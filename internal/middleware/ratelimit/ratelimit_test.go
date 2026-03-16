package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// mockLimiter implements Limiter for testing.
type mockLimiter struct {
	allowed       bool
	remaining     int
	resetAt       time.Time
	err           error
	lastKey       string
	lastAlgorithm Algorithm
}

func (m *mockLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration, algorithm Algorithm) (bool, int, time.Time, error) {
	m.lastKey = key
	m.lastAlgorithm = algorithm
	return m.allowed, m.remaining, m.resetAt, m.err
}

func newTestMetrics() *observability.MetricsCollector {
	reg := prometheus.NewRegistry()
	return observability.NewMetricsCollector(reg)
}

func TestNewRateLimitMiddleware_AllowedRequest(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 9, resetAt: resetTime}
	metrics := newTestMetrics()

	cfg := &config.RateLimitConfig{
		Requests:  10,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	handler := NewRateLimitMiddleware(cfg, "test-route", "my-limiter", ml, metrics, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
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

func TestNewRateLimitMiddleware_RejectedRequest(t *testing.T) {
	resetTime := time.Now().Add(30 * time.Second)
	ml := &mockLimiter{allowed: false, remaining: 0, resetAt: resetTime}
	metrics := newTestMetrics()

	cfg := &config.RateLimitConfig{
		Requests:  10,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	handler := NewRateLimitMiddleware(cfg, "test-route", "my-limiter", ml, metrics, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called when rate limited")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}
}

func TestNewRateLimitMiddleware_ResponseHeaders_AlwaysSet(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 5, resetAt: resetTime}

	cfg := &config.RateLimitConfig{
		Requests:  100,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	called := false
	handler := NewRateLimitMiddleware(cfg, "svc", "limiter", ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "10.0.0.1:5000"
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

func TestNewRateLimitMiddleware_429ResponseFormat(t *testing.T) {
	resetTime := time.Now().Add(45 * time.Second)
	ml := &mockLimiter{allowed: false, remaining: 0, resetAt: resetTime}

	cfg := &config.RateLimitConfig{
		Requests:  5,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	handler := NewRateLimitMiddleware(cfg, "svc", "limiter", ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
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
	// 10.0.0.1 is NOT trusted -> return 10.0.0.1.
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

	// All XFF entries are trusted -> fall back to RemoteAddr.
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

func TestNewRateLimitMiddleware_LimiterError_FailOpen(t *testing.T) {
	ml := &mockLimiter{err: errors.New("redis connection refused")}

	cfg := &config.RateLimitConfig{
		Requests:  10,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	called := false
	handler := NewRateLimitMiddleware(cfg, "svc", "limiter", ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected handler to be called (fail open)")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestNewRateLimitMiddleware_LimiterError_FailOpen_MetricIncremented(t *testing.T) {
	ml := &mockLimiter{err: errors.New("redis connection refused")}
	metrics := newTestMetrics()

	cfg := &config.RateLimitConfig{
		Requests:  10,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	handler := NewRateLimitMiddleware(cfg, "svc", "limiter", ml, metrics, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Verify the fail-open metric was incremented.
	m := &dto.Metric{}
	if err := metrics.RateLimitErrors.WithLabelValues("svc", "limiter").Write(m); err != nil {
		t.Fatalf("failed to read metric: %v", err)
	}
	if got := m.GetCounter().GetValue(); got != 1 {
		t.Fatalf("expected RateLimitErrors counter=1, got %v", got)
	}
}

func TestNewRateLimitMiddleware_CompositeKey(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 9, resetAt: resetTime}

	cfg := &config.RateLimitConfig{
		Requests:  100,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "sliding_window",
	}

	handler := NewRateLimitMiddleware(cfg, "users-api", "ip-limiter", ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expected := "rl:sliding_window:users-api:ip-limiter:10.0.0.5"
	if ml.lastKey != expected {
		t.Fatalf("expected composite key %q, got %q", expected, ml.lastKey)
	}
}

func TestNewRateLimitMiddleware_PassesAlgorithmToLimiter(t *testing.T) {
	resetTime := time.Now().Add(60 * time.Second)
	ml := &mockLimiter{allowed: true, remaining: 9, resetAt: resetTime}

	cfg := &config.RateLimitConfig{
		Requests:  50,
		Window:    time.Minute,
		KeySource: "ip",
		Algorithm: "leaky_bucket",
	}

	handler := NewRateLimitMiddleware(cfg, "lb-route", "my-limiter", ml, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if ml.lastAlgorithm != AlgorithmLeakyBucket {
		t.Fatalf("expected algorithm %q, got %q", AlgorithmLeakyBucket, ml.lastAlgorithm)
	}
	expectedKey := "rl:leaky_bucket:lb-route:my-limiter:10.0.0.5"
	if ml.lastKey != expectedKey {
		t.Fatalf("expected composite key %q, got %q", expectedKey, ml.lastKey)
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

func TestNewRateLimitMiddleware_NilConfig(t *testing.T) {
	called := false
	mw := NewRateLimitMiddleware(nil, "route", "mw", nil, nil, nil)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected pass-through handler to call next")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestExtractKey_Claim_Present(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	ctx := auth.WithAuthResult(req.Context(), &auth.AuthResult{
		Claims: map[string]any{"tenant_id": "t-123"},
	})
	req = req.WithContext(ctx)

	key := extractKey(req, "claim:tenant_id", nil)
	if key != "t-123" {
		t.Fatalf("expected t-123, got %s", key)
	}
}

func TestExtractKey_Claim_NoAuthResult(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"

	key := extractKey(req, "claim:tenant_id", nil)
	if key != "1.2.3.4" {
		t.Fatalf("expected IP fallback 1.2.3.4, got %s", key)
	}
}

func TestExtractKey_Claim_MissingClaim(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	ctx := auth.WithAuthResult(req.Context(), &auth.AuthResult{
		Claims: map[string]any{"sub": "user-1"},
	})
	req = req.WithContext(ctx)

	key := extractKey(req, "claim:tenant_id", nil)
	if key != "1.2.3.4" {
		t.Fatalf("expected IP fallback 1.2.3.4, got %s", key)
	}
}

func TestExtractKey_Claim_NonStringValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	ctx := auth.WithAuthResult(req.Context(), &auth.AuthResult{
		Claims: map[string]any{"org_id": float64(42)},
	})
	req = req.WithContext(ctx)

	key := extractKey(req, "claim:org_id", nil)
	if key != "42" {
		t.Fatalf("expected 42, got %s", key)
	}
}

func TestExtractKey_Claim_BoolValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	ctx := auth.WithAuthResult(req.Context(), &auth.AuthResult{
		Claims: map[string]any{"is_admin": true},
	})
	req = req.WithContext(ctx)

	key := extractKey(req, "claim:is_admin", nil)
	if key != "true" {
		t.Fatalf("expected true, got %s", key)
	}
}

func TestExtractKey_Claim_ComplexValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	ctx := auth.WithAuthResult(req.Context(), &auth.AuthResult{
		Claims: map[string]any{"roles": []any{"a", "b"}},
	})
	req = req.WithContext(ctx)

	key := extractKey(req, "claim:roles", nil)
	if key != `["a","b"]` {
		t.Fatalf("expected [\"a\",\"b\"], got %s", key)
	}
}

func TestExtractKey_Header_LongValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	longVal := strings.Repeat("x", 1000)
	req.Header.Set("X-Tenant-ID", longVal)

	key := extractKey(req, "header:X-Tenant-ID", nil)
	if len(key) != 32 {
		t.Fatalf("expected 32-char hex hash for long header, got %d chars: %s", len(key), key)
	}
}

func TestExtractKey_Header_ShortValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	shortVal := strings.Repeat("x", 100)
	req.Header.Set("X-Tenant-ID", shortVal)

	key := extractKey(req, "header:X-Tenant-ID", nil)
	if key != shortVal {
		t.Fatalf("expected short header value to be returned as-is, got %s", key)
	}
}
