package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
)

// corsOverrideMarker is an internal response header used to signal that a
// per-route CORS override has been applied. It is removed before the response
// is sent to the client.
const corsOverrideMarker = "X-Gateway-Cors-Override"

// CORS returns a middleware that handles CORS preflight requests (returning
// 204 without forwarding upstream) and injects CORS headers on regular
// requests using the provided global configuration.
func CORS(globalCfg config.CORSConfig) Middleware {
	if isWildcard(globalCfg.AllowedOrigins) && globalCfg.AllowCredentials {
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
				handlePreflight(w, origin, globalCfg)
				return
			}

			crw := &corsResponseWriter{
				ResponseWriter: w,
				origin:         origin,
				cfg:            globalCfg,
				isOverride:     false,
			}
			next.ServeHTTP(crw, r)
		})
	}
}

// CORSOverride returns a per-route middleware that completely replaces the
// global CORS configuration for matching requests. It does not handle
// preflights (those are handled by the global CORS middleware before routing).
func CORSOverride(routeCfg config.CORSConfig) Middleware {
	if isWildcard(routeCfg.AllowedOrigins) && routeCfg.AllowCredentials {
		slog.Warn("CORS: route override has wildcard origin with allow_credentials; will use requesting origin instead of *")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			crw := &corsResponseWriter{
				ResponseWriter: w,
				origin:         origin,
				cfg:            routeCfg,
				isOverride:     true,
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
	isOverride  bool
}

func (crw *corsResponseWriter) WriteHeader(code int) {
	if !crw.headersSent {
		crw.headersSent = true
		h := crw.ResponseWriter.Header()
		if crw.isOverride {
			// Per-route override: inject headers and set marker so the
			// outer (global) corsResponseWriter skips its own injection.
			injectCORSHeaders(crw.ResponseWriter, crw.origin, crw.cfg)
			h.Set(corsOverrideMarker, "1")
		} else {
			if h.Get(corsOverrideMarker) != "" {
				// Per-route override already applied — clean up marker.
				h.Del(corsOverrideMarker)
			} else {
				injectCORSHeaders(crw.ResponseWriter, crw.origin, crw.cfg)
			}
		}
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
