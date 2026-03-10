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
	"github.com/NextSolutionCUU/api-gateway/internal/observability"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

// Limiter checks whether a request identified by key should be allowed.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, remaining int, resetAt time.Time, err error)
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

// RateLimit returns a middleware that enforces per-route rate limits using the
// provided Limiter backend. It reads rate limit configuration from the matched
// route context. Routes without rate_limit config pass through unchanged.
func RateLimit(limiter Limiter, metrics *observability.MetricsCollector, trustedProxies []*net.IPNet) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mr := router.RouteFromContext(r.Context())
			if mr == nil || mr.Config.RateLimit == nil {
				next.ServeHTTP(w, r)
				return
			}

			rl := mr.Config.RateLimit
			extracted := extractKey(r, rl.KeySource, trustedProxies)
			compositeKey := "rl:" + mr.Config.Name + ":" + extracted

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
					metrics.RateLimitRejected.WithLabelValues(mr.Config.Name).Inc()
				}
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractKey resolves a rate-limit key from the request based on keySource.
// Supported formats: "ip", "header:<name>".
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
