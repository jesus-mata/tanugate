package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
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
	handler := RateLimit(ml, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimit(ml, metrics)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimit(ml, metrics)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := RateLimit(ml, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimit(ml, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	key := extractKey(req, "ip")
	if key != "192.168.1.1" {
		t.Fatalf("expected 192.168.1.1, got %s", key)
	}
}

func TestKeyExtract_IP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 172.16.0.1, 192.168.1.1")

	key := extractKey(req, "ip")
	if key != "10.0.0.1" {
		t.Fatalf("expected 10.0.0.1, got %s", key)
	}
}

func TestKeyExtract_Header(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	req.Header.Set("X-Tenant-ID", "tenant-42")

	key := extractKey(req, "header:X-Tenant-ID")
	if key != "tenant-42" {
		t.Fatalf("expected tenant-42, got %s", key)
	}
}

func TestKeyExtract_Header_Missing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"

	key := extractKey(req, "header:X-Tenant-ID")
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

	key := extractKey(req, "claim:tenant_id")
	if key != "org-456" {
		t.Fatalf("expected org-456, got %s", key)
	}
}

func TestKeyExtract_Claim_NoAuthContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"

	key := extractKey(req, "claim:tenant_id")
	if key != "1.2.3.4" {
		t.Fatalf("expected IP fallback 1.2.3.4, got %s", key)
	}
}

func TestRateLimit_LimiterError_FailOpen(t *testing.T) {
	ml := &mockLimiter{err: errors.New("redis connection refused")}

	called := false
	handler := RateLimit(ml, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimit(ml, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
