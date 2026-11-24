package httpserver

import (
	"io"
	"net/http"
	"time"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverIdleTimeout       = 2 * time.Minute
	streamIdleTimeout       = 2 * time.Minute
	streamDeadlineRefresh   = 5 * time.Second
	sendfileDeadlineChunk   = 1 << 20 // 1 MiB：兼顾零拷贝 sendfile 与按进度滚动写超时
)

// deadlineReader 在流式读取请求体时按进度滚动刷新连接读超时，
// 防止慢速上传无限期占用连接，同时允许正常的大文件持续传输。
type deadlineReader struct {
	rc       *http.ResponseController
	r        io.Reader
	timeout  time.Duration
	lastBump time.Time
}

func newRollingReadDeadline(w http.ResponseWriter, r io.Reader) io.Reader {
	rc := http.NewResponseController(w)
	now := time.Now()
	_ = rc.SetReadDeadline(now.Add(streamIdleTimeout))
	return &deadlineReader{
		rc:       rc,
		r:        r,
		timeout:  streamIdleTimeout,
		lastBump: now,
	}
}

func (dr *deadlineReader) Read(p []byte) (int, error) {
	n, err := dr.r.Read(p)
	if n > 0 {
		if now := time.Now(); now.Sub(dr.lastBump) >= streamDeadlineRefresh {
			_ = dr.rc.SetReadDeadline(now.Add(dr.timeout))
			dr.lastBump = now
		}
	}
	return n, err
}

// deadlineResponseWriter 在流式下载写出期间按进度滚动刷新连接写超时，
// 并分块委托底层 io.ReaderFrom（保留零拷贝 sendfile 能力）。
type deadlineResponseWriter struct {
	http.ResponseWriter
	rc       *http.ResponseController
	timeout  time.Duration
	lastBump time.Time
}

func newRollingWriteResponseWriter(w http.ResponseWriter) *deadlineResponseWriter {
	rc := http.NewResponseController(w)
	now := time.Now()
	_ = rc.SetWriteDeadline(now.Add(streamIdleTimeout))
	return &deadlineResponseWriter{
		ResponseWriter: w,
		rc:             rc,
		timeout:        streamIdleTimeout,
		lastBump:       now,
	}
}

func (dw *deadlineResponseWriter) Unwrap() http.ResponseWriter {
	return dw.ResponseWriter
}

func (dw *deadlineResponseWriter) bump() {
	if now := time.Now(); now.Sub(dw.lastBump) >= streamDeadlineRefresh {
		_ = dw.rc.SetWriteDeadline(now.Add(dw.timeout))
		dw.lastBump = now
	}
}

func (dw *deadlineResponseWriter) Write(p []byte) (int, error) {
	n, err := dw.ResponseWriter.Write(p)
	if n > 0 {
		dw.bump()
	}
	return n, err
}

func (dw *deadlineResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	rf, hasRF := dw.ResponseWriter.(io.ReaderFrom)
	if !hasRF {
		return io.Copy(dw.ResponseWriter, r)
	}
	var total int64
	for {
		n, err := rf.ReadFrom(io.LimitReader(r, sendfileDeadlineChunk))
		total += n
		if n > 0 {
			dw.bump()
		}
		if err != nil {
			return total, err
		}
		if n < sendfileDeadlineChunk {
			return total, nil
		}
	}
}

func serveContentWithDeadline(w http.ResponseWriter, r *http.Request, name string, modtime time.Time, content io.ReadSeeker) {
	dw := newRollingWriteResponseWriter(w)
	defer func() {
		// 恢复无限制截止时间，避免 keep-alive 复用连接沿用旧写超时。
		_ = dw.rc.SetWriteDeadline(time.Time{})
	}()
	http.ServeContent(dw, r, name, modtime, content)
}
