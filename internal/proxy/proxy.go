package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/router"
)

// NewProxyHandler creates an http.Handler that reverse-proxies requests to the
// upstream service described by routeCfg. It supports path rewriting via named
// capture groups extracted by the router.
//
// If transport is non-nil it is used as the underlying RoundTripper, allowing
// callers to share a single transport across routes. When transport is nil a
// default http.Transport is created for backward compatibility (useful in
// tests).
func NewProxyHandler(routeCfg *config.RouteConfig, transport http.RoundTripper) http.Handler {
	upstream, err := url.Parse(routeCfg.Upstream.URL)
	if err != nil {
		slog.Error("failed to parse upstream URL",
			"route", routeCfg.Name,
			"url", routeCfg.Upstream.URL,
			"error", err,
		)
		// Return a handler that always responds with 502 so the gateway can
		// still start even when one route has a misconfigured upstream.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "bad_gateway",
				"message": "Upstream service unavailable",
			})
		})
	}

	if transport == nil {
		transport = &http.Transport{
			ResponseHeaderTimeout: routeCfg.Upstream.Timeout,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
		}
	}

	timeout := routeCfg.Upstream.Timeout

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.Host = upstream.Host

			matched := router.RouteFromContext(req.Context())

			if routeCfg.Upstream.PathRewrite != "" && matched != nil && matched.PathParams != nil {
				rewritten := routeCfg.Upstream.PathRewrite
				for key, value := range matched.PathParams {
					rewritten = strings.ReplaceAll(rewritten, "{"+key+"}", value)
				}
				req.URL.Path = rewritten
			}

			// Clear RawPath so Go re-encodes from the (possibly rewritten) Path.
			req.URL.RawPath = ""

			slog.Debug("proxying request",
				"route", routeCfg.Name,
				"method", req.Method,
				"upstream_url", req.URL.String(),
			)
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("proxy error",
				"route", routeCfg.Name,
				"method", r.Method,
				"path", r.URL.Path,
				"error", err,
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "bad_gateway",
				"message": "Upstream service unavailable",
			})
		},
	}

	// When using a shared transport, apply per-route timeout via context
	// rather than ResponseHeaderTimeout (which is transport-wide).
	if timeout > 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			proxy.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	return proxy
}
