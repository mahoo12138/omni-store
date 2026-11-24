package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 0

// RequestIDFrom 从 context 取 request_id，没有时返回空字符串。
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req_00000000"
	}
	return "req_" + hex.EncodeToString(b)
}

// WithRequestID 为每个请求生成 req_xxx 并注入 context 和响应头。
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// WithSecurityHeaders adds browser hardening headers to every response. Raw
// user content receives an additional restrictive CSP in its own handler.
func WithSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

func (sr *statusRecorder) WroteHeader() bool {
	return sr.wroteHeader
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.wroteHeader {
		return
	}
	sr.status = code
	sr.wroteHeader = true
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.wroteHeader = true
	}
	return sr.ResponseWriter.Write(b)
}

// ReadFrom 透传底层 io.ReaderFrom，保证 http.ServeContent 等下载路径可走 sendfile。
func (sr *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if !sr.wroteHeader {
		sr.wroteHeader = true
	}
	if rf, ok := sr.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(sr.ResponseWriter, r)
}

// WithAccessLog 记录基础访问日志。
func WithAccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestIDFrom(r.Context()),
		)
	})
}

type headerWriteState interface {
	WroteHeader() bool
}

// WithRecover 捕获 handler panic，返回统一 INTERNAL_ERROR。
// 遵循 net/http 约定放行 http.ErrAbortHandler，且在响应头已写出后不再追加破坏流的 JSON。
func WithRecover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, ok := w.(headerWriteState)
		if !ok {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			w = rec
			state = rec
		}
		defer func() {
			if err := recover(); err != nil {
				if err == http.ErrAbortHandler {
					panic(err)
				}
				logger.Error("panic",
					"err", err,
					"path", r.URL.Path,
					"request_id", RequestIDFrom(r.Context()),
					"stack", string(debug.Stack()),
				)
				if !state.WroteHeader() {
					WriteError(w, r, CodeInternalError, "服务器内部错误", nil)
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}
