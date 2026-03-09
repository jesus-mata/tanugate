package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/auth"
	"github.com/NextSolutionCUU/api-gateway/internal/observability"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
	"github.com/prometheus/client_golang/prometheus"
)

// mockLimiter implements Limiter for testing.
type mockLimiter struct {
	allowed   bool
	remaining int
	resetAt   time.Time
	err       error
	lastKey   string
}

func (m *mockLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, int, time.Time, error) {
	m.lastKey = key
	return m.allowed, m.remaining, m.resetAt, m.err
}

func newTestMetrics() *observability.MetricsCollector {
	reg := prometheus.NewRegistry()
	return observability.NewMetricsCollector(reg)
}

func withRoute(r *http.Request, route *config.RouteConfig) *http.Request {
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: route,
	})
	return r.WithContext(ctx)
}

func TestRateLimit_SkipsWhenNoConfig(t *testing.T) {
	ml := &mockLimiter{allowed: true}
	handler := RateLimit(ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No route context at all.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "" {
		t.Fatal("expected no rate limit headers")
	}

	// Route context with nil RateLimit.
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req = withRoute(req, &config.RouteConfig{Name: "test"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "" {
		t.Fatal("expected no rate limit headers when RateLimit is nil")
	}
}

func TestRateLimit_AllowedRequest(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 9, resetAt: resetTime}
	metrics := newTestMetrics()

	handler := RateLimit(ml, metrics, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	req = withRoute(req, &config.RouteConfig{
		Name: "test-route",
		RateLimit: &config.RouteLimitConfig{
			RequestsPerWindow: 10,
			Window:            time.Minute,
			KeySource:         "ip",
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "10" {
		t.Fatalf("expected X-RateLimit-Limit=10, got %s", rec.Header().Get("X-RateLimit-Limit"))
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "9" {
		t.Fatalf("expected X-RateLimit-Remaining=9, got %s", rec.Header().Get("X-RateLimit-Remaining"))
	}
	if rec.Header().Get("X-RateLimit-Reset") == "" {
		t.Fatal("expected X-RateLimit-Reset to be set")
	}
}

func TestRateLimit_RejectedRequest(t *testing.T) {
	resetTime := time.Now().Add(30 * time.Second)
	ml := &mockLimiter{allowed: false, remaining: 0, resetAt: resetTime}
	metrics := newTestMetrics()

	handler := RateLimit(ml, metrics, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called when rate limited")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	req = withRoute(req, &config.RouteConfig{
		Name: "test-route",
		RateLimit: &config.RouteLimitConfig{
			RequestsPerWindow: 10,
			Window:            time.Minute,
			KeySource:         "ip",
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}
}

func TestRateLimit_ResponseHeaders_AlwaysSet(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 5, resetAt: resetTime}

	called := false
	handler := RateLimit(ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	req = withRoute(req, &config.RouteConfig{
		Name: "svc",
		RateLimit: &config.RouteLimitConfig{
			RequestsPerWindow: 100,
			Window:            time.Minute,
			KeySource:         "ip",
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected handler to be called")
	}
	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if rec.Header().Get(h) == "" {
			t.Fatalf("expected header %s to be set on allowed response", h)
		}
	}
}

func TestRateLimit_429ResponseFormat(t *testing.T) {
	resetTime := time.Now().Add(45 * time.Second)
	ml := &mockLimiter{allowed: false, remaining: 0, resetAt: resetTime}

	handler := RateLimit(ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	req = withRoute(req, &config.RouteConfig{
		Name: "svc",
		RateLimit: &config.RouteLimitConfig{
			RequestsPerWindow: 5,
			Window:            time.Minute,
			KeySource:         "ip",
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type, got %s", rec.Header().Get("Content-Type"))
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["error"] != "rate_limit_exceeded" {
		t.Fatalf("expected error=rate_limit_exceeded, got %v", body["error"])
	}
	if body["message"] == nil {
		t.Fatal("expected message field in response")
	}
	if body["retry_after"] == nil {
		t.Fatal("expected retry_after field in response")
	}
}

func TestKeyExtract_IP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"

	key := extractKey(req, "ip", nil)
	if key != "192.168.1.1" {
		t.Fatalf("expected 192.168.1.1, got %s", key)
	}
}

func TestKeyExtract_IP_NoTrustedProxies(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 172.16.0.1, 192.168.1.1")

	// Without trusted proxies, XFF should be ignored entirely.
	key := extractKey(req, "ip", nil)
	if key != "127.0.0.1" {
		t.Fatalf("expected 127.0.0.1 (RemoteAddr), got %s", key)
	}
}

func TestKeyExtract_IP_WithTrustedProxies(t *testing.T) {
	// Trust 192.168.1.0/24 and 172.16.0.0/16.
	trusted, err := ParseTrustedProxies([]string{"192.168.1.0/24", "172.16.0.0/16"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:12345"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 172.16.0.1, 192.168.1.1")

	// Walk right-to-left: 192.168.1.1 is trusted, 172.16.0.1 is trusted,
	// 10.0.0.1 is NOT trusted → return 10.0.0.1.
	key := extractKey(req, "ip", trusted)
	if key != "10.0.0.1" {
		t.Fatalf("expected 10.0.0.1, got %s", key)
	}
}

func TestKeyExtract_IP_UntrustedRemoteAddr(t *testing.T) {
	// Trust only 192.168.1.0/24.
	trusted, err := ParseTrustedProxies([]string{"192.168.1.0/24"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:12345" // NOT a trusted proxy
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 192.168.1.1")

	// RemoteAddr is not trusted, so XFF must be ignored entirely.
	key := extractKey(req, "ip", trusted)
	if key != "8.8.8.8" {
		t.Fatalf("expected 8.8.8.8 (RemoteAddr, untrusted direct client), got %s", key)
	}
}

func TestKeyExtract_IP_AllTrusted(t *testing.T) {
	// Trust everything in 10.0.0.0/8.
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:12345"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 172.16.0.1, 192.168.1.1")

	// All XFF entries are trusted → fall back to RemoteAddr.
	key := extractKey(req, "ip", trusted)
	if key != "192.168.1.50" {
		t.Fatalf("expected 192.168.1.50 (RemoteAddr fallback), got %s", key)
	}
}

func TestKeyExtract_Header(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	req.Header.Set("X-Tenant-ID", "tenant-42")

	key := extractKey(req, "header:X-Tenant-ID", nil)
	if key != "tenant-42" {
		t.Fatalf("expected tenant-42, got %s", key)
	}
}

func TestKeyExtract_Header_Missing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"

	key := extractKey(req, "header:X-Tenant-ID", nil)
	if key != "1.2.3.4" {
		t.Fatalf("expected IP fallback 1.2.3.4, got %s", key)
	}
}

func TestKeyExtract_Claim(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	ctx := auth.WithAuthResult(req.Context(), &auth.AuthResult{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":       "user-123",
			"tenant_id": "org-456",
		},
	})
	req = req.WithContext(ctx)

	key := extractKey(req, "claim:tenant_id", nil)
	if key != "org-456" {
		t.Fatalf("expected org-456, got %s", key)
	}
}

func TestKeyExtract_Claim_NoAuthContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"

	key := extractKey(req, "claim:tenant_id", nil)
	if key != "1.2.3.4" {
		t.Fatalf("expected IP fallback 1.2.3.4, got %s", key)
	}
}

func TestRateLimit_LimiterError_FailOpen(t *testing.T) {
	ml := &mockLimiter{err: errors.New("redis connection refused")}

	called := false
	handler := RateLimit(ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	req = withRoute(req, &config.RouteConfig{
		Name: "svc",
		RateLimit: &config.RouteLimitConfig{
			RequestsPerWindow: 10,
			Window:            time.Minute,
			KeySource:         "ip",
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected handler to be called (fail open)")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRateLimit_CompositeKey(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 9, resetAt: resetTime}

	handler := RateLimit(ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	req = withRoute(req, &config.RouteConfig{
		Name: "users-api",
		RateLimit: &config.RouteLimitConfig{
			RequestsPerWindow: 100,
			Window:            time.Minute,
			KeySource:         "ip",
		},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expected := "users-api:10.0.0.5"
	if ml.lastKey != expected {
		t.Fatalf("expected composite key %q, got %q", expected, ml.lastKey)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		nets, err := ParseTrustedProxies(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if nets != nil {
			t.Fatalf("expected nil, got %v", nets)
		}
	})

	t.Run("valid CIDRs", func(t *testing.T) {
		nets, err := ParseTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 2 {
			t.Fatalf("expected 2 networks, got %d", len(nets))
		}
		// 10.1.2.3 should be in first network.
		if !nets[0].Contains(net.ParseIP("10.1.2.3")) {
			t.Fatal("expected 10.1.2.3 to be in 10.0.0.0/8")
		}
	})

	t.Run("bare IPv4", func(t *testing.T) {
		nets, err := ParseTrustedProxies([]string{"192.168.1.1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 1 {
			t.Fatalf("expected 1 network, got %d", len(nets))
		}
		if !nets[0].Contains(net.ParseIP("192.168.1.1")) {
			t.Fatal("expected 192.168.1.1 to match")
		}
		if nets[0].Contains(net.ParseIP("192.168.1.2")) {
			t.Fatal("expected 192.168.1.2 NOT to match bare IP /32")
		}
	})

	t.Run("bare IPv6", func(t *testing.T) {
		nets, err := ParseTrustedProxies([]string{"::1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 1 {
			t.Fatalf("expected 1 network, got %d", len(nets))
		}
		if !nets[0].Contains(net.ParseIP("::1")) {
			t.Fatal("expected ::1 to match")
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		_, err := ParseTrustedProxies([]string{"not-an-ip"})
		if err == nil {
			t.Fatal("expected error for invalid input")
		}
	})

	t.Run("invalid CIDR", func(t *testing.T) {
		_, err := ParseTrustedProxies([]string{"10.0.0.0/33"})
		if err == nil {
			t.Fatal("expected error for invalid CIDR")
		}
	})
}
