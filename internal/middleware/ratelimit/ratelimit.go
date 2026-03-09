package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/middleware"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/auth"
	"github.com/NextSolutionCUU/api-gateway/internal/observability"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

// Limiter checks whether a request identified by key should be allowed.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, remaining int, resetAt time.Time, err error)
}

// RateLimit returns a middleware that enforces per-route rate limits using the
// provided Limiter backend. It reads rate limit configuration from the matched
// route context. Routes without rate_limit config pass through unchanged.
func RateLimit(limiter Limiter, metrics *observability.MetricsCollector) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mr := router.RouteFromContext(r.Context())
			if mr == nil || mr.Config.RateLimit == nil {
				next.ServeHTTP(w, r)
				return
			}

			rl := mr.Config.RateLimit
			extracted := extractKey(r, rl.KeySource)
			compositeKey := mr.Config.Name + ":" + extracted

			allowed, remaining, resetAt, err := limiter.Allow(
				r.Context(),
				compositeKey,
				rl.RequestsPerWindow,
				rl.Window,
			)

			if err != nil {
				slog.Error("rate limiter backend error, failing open",
					"route", mr.Config.Name,
					"key", compositeKey,
					"error", err,
				)
				next.ServeHTTP(w, r)
				return
			}

			resetUnix := resetAt.Unix()
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", rl.RequestsPerWindow))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetUnix))

			if !allowed {
				retryAfter := max(int(time.Until(resetAt).Seconds()), 1)
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]any{
					"error":       "rate_limit_exceeded",
					"message":     fmt.Sprintf("Rate limit exceeded. Try again in %d seconds.", retryAfter),
					"retry_after": retryAfter,
				})
				if metrics != nil {
					metrics.RateLimitRejected.WithLabelValues(mr.Config.Name).Inc()
				}
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractKey resolves a rate-limit key from the request based on keySource.
// Supported formats: "ip", "header:<name>", "claim:<name>".
func extractKey(r *http.Request, keySource string) string {
	switch {
	case keySource == "" || keySource == "ip":
		return extractIP(r)

	case strings.HasPrefix(keySource, "header:"):
		name := strings.TrimPrefix(keySource, "header:")
		val := r.Header.Get(name)
		if val == "" {
			slog.Warn("rate limit header key empty, falling back to IP",
				"header", name,
			)
			return extractIP(r)
		}
		return val

	case strings.HasPrefix(keySource, "claim:"):
		claimName := strings.TrimPrefix(keySource, "claim:")
		ar := auth.ResultFromContext(r.Context())
		if ar == nil || ar.Claims == nil {
			slog.Warn("rate limit claim key unavailable (no auth context), falling back to IP",
				"claim", claimName,
			)
			return extractIP(r)
		}
		val, ok := ar.Claims[claimName]
		if !ok {
			slog.Warn("rate limit claim key not found, falling back to IP",
				"claim", claimName,
			)
			return extractIP(r)
		}
		return fmt.Sprintf("%v", val)

	default:
		return extractIP(r)
	}
}

// extractIP returns the client IP from X-Forwarded-For (first entry) or
// RemoteAddr as a fallback.
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
