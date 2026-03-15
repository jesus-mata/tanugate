package observability_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/observability"
)

type mockChecker struct {
	err error
}

func (m *mockChecker) HealthCheck(_ context.Context) error {
	return m.err
}

func TestHealth_BasicResponse(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "memory"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	h := observability.HealthHandler(&cfgPtr, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp observability.HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Status != "up" {
		t.Errorf("expected status=up, got %s", resp.Status)
	}
	if resp.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestHealth_RedisNotConfigured(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "redis"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	h := observability.HealthHandler(&cfgPtr, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp observability.HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Checks["redis"] != "not_configured" {
		t.Errorf("expected redis=not_configured, got %s", resp.Checks["redis"])
	}
}

func TestHealth_RedisUp(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "redis"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	h := observability.HealthHandler(&cfgPtr, &mockChecker{err: nil})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp observability.HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Status != "up" {
		t.Errorf("expected status=up, got %s", resp.Status)
	}
	if resp.Checks["redis"] != "up" {
		t.Errorf("expected redis=up, got %s", resp.Checks["redis"])
	}
}

func TestHealth_RedisDown(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "redis"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	h := observability.HealthHandler(&cfgPtr, &mockChecker{err: errors.New("connection refused")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp observability.HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Status != "degraded" {
		t.Errorf("expected status=degraded, got %s", resp.Status)
	}
	if resp.Checks["redis"] != "down" {
		t.Errorf("expected redis=down, got %s", resp.Checks["redis"])
	}
}

func TestHealth_ContentType(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "memory"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	h := observability.HealthHandler(&cfgPtr, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}

func TestHealth_NoChecksField(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "memory"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	h := observability.HealthHandler(&cfgPtr, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var raw map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&raw)

	if _, ok := raw["checks"]; ok {
		t.Error("expected no checks field for memory backend")
	}
}

func TestHealth_AtomicConfigSwap(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "memory"
	var cfgPtr atomic.Pointer[config.GatewayConfig]
	cfgPtr.Store(cfg)

	checker := &mockChecker{err: nil}
	h := observability.HealthHandler(&cfgPtr, checker)

	// First request: memory backend -> no checks field, status=up.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp1 observability.HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp1)
	if resp1.Status != "up" {
		t.Errorf("expected status=up, got %s", resp1.Status)
	}
	if resp1.Checks != nil {
		t.Error("expected no checks field for memory backend")
	}

	// Swap config to redis backend.
	cfg2 := &config.GatewayConfig{}
	cfg2.RateLimit.Backend = "redis"
	cfgPtr.Store(cfg2)

	// Second request: redis backend -> checks field should appear.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
	var resp2 observability.HealthResponse
	_ = json.NewDecoder(rec2.Body).Decode(&resp2)
	if resp2.Checks == nil {
		t.Fatal("expected checks field after swapping to redis backend")
	}
	if resp2.Checks["redis"] != "up" {
		t.Errorf("expected redis=up, got %s", resp2.Checks["redis"])
	}
}
