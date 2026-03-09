package middleware

import (
	"net/http"
)

// WrappedResponseWriter captures the status code and bytes written by the handler.
type WrappedResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
	wroteHeader  bool
}

// WrapResponseWriter wraps w. If w is already a *WrappedResponseWriter it is
// returned as-is (idempotent).
func WrapResponseWriter(w http.ResponseWriter) *WrappedResponseWriter {
	if ww, ok := w.(*WrappedResponseWriter); ok {
		return ww
	}
	return &WrappedResponseWriter{ResponseWriter: w}
}

func (w *WrappedResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *WrappedResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

// StatusCode returns the captured status code, or 200 if WriteHeader was never called.
func (w *WrappedResponseWriter) StatusCode() int {
	if !w.wroteHeader {
		return http.StatusOK
	}
	return w.statusCode
}

// BytesWritten returns the total number of response bytes written.
func (w *WrappedResponseWriter) BytesWritten() int {
	return w.bytesWritten
}

// Flush delegates to the underlying writer if it supports http.Flusher.
func (w *WrappedResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController compatibility.
func (w *WrappedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
