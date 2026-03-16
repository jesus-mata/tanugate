package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jesus-mata/tanugate/internal/config"
)

// CORSMiddleware returns a middleware that handles CORS preflight requests
// (returning 204 without forwarding upstream) and injects CORS headers on
// regular cross-origin requests using the provided configuration.
func CORSMiddleware(cfg config.CORSConfig) Middleware {
	if isWildcard(cfg.AllowedOrigins) && cfg.AllowCredentials {
		slog.Warn("CORS: wildcard origin with allow_credentials is invalid per spec; will use requesting origin instead of *")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			if isPreflight(r) {
				handlePreflight(w, origin, cfg)
				return
			}

			crw := &corsResponseWriter{
				ResponseWriter: w,
				origin:         origin,
				cfg:            cfg,
			}
			next.ServeHTTP(crw, r)
		})
	}
}

// corsResponseWriter intercepts WriteHeader to inject CORS headers before
// the status code is sent to the client.
type corsResponseWriter struct {
	http.ResponseWriter
	origin      string
	cfg         config.CORSConfig
	headersSent bool
}

func (crw *corsResponseWriter) WriteHeader(code int) {
	if !crw.headersSent {
		crw.headersSent = true
		injectCORSHeaders(crw.ResponseWriter, crw.origin, crw.cfg)
	}
	crw.ResponseWriter.WriteHeader(code)
}

func (crw *corsResponseWriter) Write(b []byte) (int, error) {
	if !crw.headersSent {
		crw.WriteHeader(http.StatusOK)
	}
	return crw.ResponseWriter.Write(b)
}

// Flush delegates to the underlying writer if it supports http.Flusher.
func (crw *corsResponseWriter) Flush() {
	if f, ok := crw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController.
func (crw *corsResponseWriter) Unwrap() http.ResponseWriter {
	return crw.ResponseWriter
}

func isWildcard(origins []string) bool {
	for _, o := range origins {
		if o == "*" {
			return true
		}
	}
	return false
}

func isOriginAllowed(origin string, allowedOrigins []string) bool {
	for _, o := range allowedOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

func resolveAllowOrigin(origin string, cfg config.CORSConfig) string {
	if !isOriginAllowed(origin, cfg.AllowedOrigins) {
		return ""
	}
	if isWildcard(cfg.AllowedOrigins) && !cfg.AllowCredentials {
		return "*"
	}
	return origin
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions &&
		r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != ""
}

func handlePreflight(w http.ResponseWriter, origin string, cfg config.CORSConfig) {
	h := w.Header()
	h.Set("Vary", "Origin")

	allowOrigin := resolveAllowOrigin(origin, cfg)
	if allowOrigin == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.Set("Access-Control-Allow-Origin", allowOrigin)
	h.Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
	if len(cfg.AllowedHeaders) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
	}
	if cfg.MaxAge > 0 {
		h.Set("Access-Control-Max-Age", fmt.Sprintf("%d", cfg.MaxAge))
	}
	if cfg.AllowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	w.WriteHeader(http.StatusNoContent)
}

func injectCORSHeaders(w http.ResponseWriter, origin string, cfg config.CORSConfig) {
	h := w.Header()
	h.Set("Vary", "Origin")

	allowOrigin := resolveAllowOrigin(origin, cfg)
	if allowOrigin == "" {
		return
	}

	h.Set("Access-Control-Allow-Origin", allowOrigin)
	if cfg.AllowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if len(cfg.ExposedHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposedHeaders, ", "))
	}
}
