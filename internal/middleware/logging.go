package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

// Logging returns a middleware that logs each request and its completion using
// the provided structured logger.
func Logging(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := WrapResponseWriter(w)

			requestID := RequestIDFromContext(r.Context())

			logger.Info("request started",
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
				"request_id", requestID,
			)

			logger.Debug("request headers", "headers", r.Header)

			next.ServeHTTP(ww, r)

			route := "unknown"
			if mr := router.RouteFromContext(r.Context()); mr != nil {
				route = mr.Config.Name
			}

			latency := time.Since(start)

			logger.Info("request completed",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.StatusCode(),
				"latency_ms", latency.Milliseconds(),
				"request_id", requestID,
				"route", route,
				"remote_addr", r.RemoteAddr,
				"response_size", ww.BytesWritten(),
			)

			logger.Debug("response headers", "headers", ww.Header())
		})
	}
}
