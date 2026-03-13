package observability_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

	h := observability.HealthHandler(cfg, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp observability.HealthResponse
	json.NewDecoder(rec.Body).Decode(&resp)

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

	h := observability.HealthHandler(cfg, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var resp observability.HealthResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.Checks["redis"] != "not_configured" {
		t.Errorf("expected redis=not_configured, got %s", resp.Checks["redis"])
	}
}

func TestHealth_RedisUp(t *testing.T) {
	cfg := &config.GatewayConfig{}
	cfg.RateLimit.Backend = "redis"

	h := observability.HealthHandler(cfg, &mockChecker{err: nil})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var resp observability.HealthResponse
	json.NewDecoder(rec.Body).Decode(&resp)

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

	h := observability.HealthHandler(cfg, &mockChecker{err: errors.New("connection refused")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var resp observability.HealthResponse
	json.NewDecoder(rec.Body).Decode(&resp)

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

	h := observability.HealthHandler(cfg, nil)
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

	h := observability.HealthHandler(cfg, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var raw map[string]any
	json.NewDecoder(rec.Body).Decode(&raw)

	if _, ok := raw["checks"]; ok {
		t.Error("expected no checks field for memory backend")
	}
}
