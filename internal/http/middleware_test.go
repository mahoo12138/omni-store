package httpserver

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type readerFromRecorder struct {
	*httptest.ResponseRecorder
	usedReadFrom bool
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.usedReadFrom = true
	return io.Copy(r.ResponseRecorder, src)
}

func TestStatusRecorderUnwrapAndReadFrom(t *testing.T) {
	base := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	rec := &statusRecorder{ResponseWriter: base, status: http.StatusOK}

	if rec.Unwrap() != base {
		t.Fatal("statusRecorder.Unwrap did not return underlying ResponseWriter")
	}
	// 使用不实现 io.WriterTo 的 reader，确保 io.Copy 走 statusRecorder.ReadFrom 路径。
	if _, err := io.Copy(rec, struct{ io.Reader }{strings.NewReader("payload")}); err != nil {
		t.Fatalf("io.Copy failed: %v", err)
	}
	if !base.usedReadFrom {
		t.Fatal("statusRecorder did not forward io.ReaderFrom")
	}
	if !rec.WroteHeader() || base.Body.String() != "payload" {
		t.Fatalf("unexpected recorder state: wroteHeader=%v body=%q", rec.WroteHeader(), base.Body.String())
	}
	// Duplicate WriteHeader must not clobber the logged status.
	rec.WriteHeader(http.StatusInternalServerError)
	if rec.status != http.StatusOK {
		t.Fatalf("duplicate WriteHeader overwrote status: got %d want %d", rec.status, http.StatusOK)
	}
}

func TestWithRecoverPropagatesErrAbortHandler(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := WithRecover(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		r := recover()
		if r != http.ErrAbortHandler {
			t.Fatalf("recovered=%v want http.ErrAbortHandler", r)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
}

func TestWithRecoverDoesNotCorruptCommittedStream(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := WithAccessLog(logger, WithRecover(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial-stream"))
		panic("boom after write")
	})))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	if rec.Body.String() != "partial-stream" {
		t.Fatalf("body=%q was corrupted after panic", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "boom after write") {
		t.Fatalf("panic was not logged: %s", logs.String())
	}
}
