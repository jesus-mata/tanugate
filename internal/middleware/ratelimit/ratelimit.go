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

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/observability"
)

// Algorithm identifies the rate limiting strategy used by a Limiter.
type Algorithm string

const (
	AlgorithmSlidingWindow Algorithm = "sliding_window"
	AlgorithmLeakyBucket   Algorithm = "leaky_bucket"
)

// Limiter checks whether a request identified by key should be allowed.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration, algorithm Algorithm) (allowed bool, remaining int, resetAt time.Time, err error)
}

// ParseTrustedProxies converts a list of CIDR strings (or bare IPs) into
// parsed []*net.IPNet values. Bare IPs are automatically converted to /32
// (IPv4) or /128 (IPv6). Returns an error if any entry is invalid.
func ParseTrustedProxies(cidrs []string) ([]*net.IPNet, error) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if !strings.Contains(cidr, "/") {
			ip := net.ParseIP(cidr)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy IP: %q", cidr)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			cidr = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}
	return nets, nil
}

// NewRateLimitMiddleware creates a rate limit middleware with config bound at
// construction time. The middleware closes over the provided configuration
// instead of reading it from route context, enabling multiple rate limit
// middleware instances per route with distinct keys and metrics.
func NewRateLimitMiddleware(cfg *config.RateLimitConfig, routeName, middlewareName string,
	limiter Limiter, metrics *observability.MetricsCollector, trustedProxies []*net.IPNet) middleware.Middleware {
	if cfg == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extracted := extractKey(r, cfg.KeySource, trustedProxies)
			algorithm := Algorithm(cfg.Algorithm)
			compositeKey := "rl:" + string(algorithm) + ":" + routeName + ":" + middlewareName + ":" + extracted

			allowed, remaining, resetAt, err := limiter.Allow(
				r.Context(),
				compositeKey,
				cfg.Requests,
				cfg.Window,
				algorithm,
			)

			if err != nil {
				slog.Error("rate limiter backend error, failing open",
					"route", routeName,
					"middleware", middlewareName,
					"key", compositeKey,
					"error", err,
				)
				if metrics != nil {
					metrics.RateLimitErrors.WithLabelValues(routeName, middlewareName).Inc()
				}
				next.ServeHTTP(w, r)
				return
			}

			resetUnix := resetAt.Unix()
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", cfg.Requests))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetUnix))

			if !allowed {
				retryAfter := max(int(resetAt.Unix()-time.Now().Unix()), 1)
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				if err := json.NewEncoder(w).Encode(map[string]any{
					"error":       "rate_limit_exceeded",
					"message":     fmt.Sprintf("Rate limit exceeded. Try again in %d seconds.", retryAfter),
					"retry_after": retryAfter,
				}); err != nil {
					slog.Debug("failed to encode 429 response body", "error", err)
				}
				if metrics != nil {
					metrics.RateLimitRejected.WithLabelValues(routeName, middlewareName).Inc()
				}
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractKey resolves a rate-limit key from the request based on keySource.
// Supported formats: "ip", "header:<name>", "claim:<name>".
func extractKey(r *http.Request, keySource string, trustedProxies []*net.IPNet) string {
	switch {
	case keySource == "" || keySource == "ip":
		return extractIP(r, trustedProxies)

	case strings.HasPrefix(keySource, "header:"):
		name := strings.TrimPrefix(keySource, "header:")
		val := r.Header.Get(name)
		if val == "" {
			slog.Warn("rate limit header key empty, falling back to IP",
				"header", name,
			)
			return extractIP(r, trustedProxies)
		}
		return val

	case strings.HasPrefix(keySource, "claim:"):
		claimName := strings.TrimPrefix(keySource, "claim:")
		ar := auth.ResultFromContext(r.Context())
		if ar == nil || ar.Claims == nil {
			slog.Warn("rate limit claim key: no auth result in context, falling back to IP", "claim", claimName)
			return extractIP(r, trustedProxies)
		}
		val, ok := ar.Claims[claimName]
		if !ok {
			slog.Warn("rate limit claim key not found, falling back to IP", "claim", claimName)
			return extractIP(r, trustedProxies)
		}
		return fmt.Sprintf("%v", val)

	default:
		return extractIP(r, trustedProxies)
	}
}

// extractIP returns the client IP address for rate-limiting purposes.
// When trustedProxies is nil or empty, X-Forwarded-For is ignored entirely and
// the IP is taken from RemoteAddr (safe default).
// When trustedProxies is configured, RemoteAddr is first checked against the
// trusted list — XFF is only consulted if the immediate connection comes from a
// trusted proxy. The X-Forwarded-For header is then walked right-to-left and
// the first untrusted IP is returned. If every entry is trusted, RemoteAddr is
// used as a fallback.
func extractIP(r *http.Request, trustedProxies []*net.IPNet) string {
	addr := remoteAddrIP(r)

	if len(trustedProxies) == 0 {
		return addr
	}

	// Only consult XFF if the immediate connection is from a trusted proxy.
	remoteIP := net.ParseIP(addr)
	if remoteIP == nil || !isTrusted(remoteIP, trustedProxies) {
		return addr
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return addr
	}

	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			slog.Warn("skipping unparseable IP in X-Forwarded-For",
				"entry", candidate,
			)
			continue
		}
		if !isTrusted(ip, trustedProxies) {
			return candidate
		}
	}

	return addr
}

// remoteAddrIP extracts the IP portion from r.RemoteAddr (host:port).
func remoteAddrIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isTrusted reports whether ip falls within any of the given networks.
func isTrusted(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
