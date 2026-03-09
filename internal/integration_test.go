package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware"
	"github.com/NextSolutionCUU/api-gateway/internal/proxy"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

func setupGateway(t *testing.T, upstream *httptest.Server) http.Handler {
	t.Helper()

	routes := []config.RouteConfig{
		{
			Name: "user-service",
			Match: config.MatchConfig{
				PathRegex: `^/api/users/(?P<id>[^/]+)$`,
				Methods:   []string{"GET", "POST"},
			},
			Upstream: config.UpstreamConfig{
				URL:         upstream.URL,
				PathRewrite: "/internal/users/${id}",
				Timeout:     5_000_000_000, // 5s
			},
		},
		{
			Name: "catch-all",
			Match: config.MatchConfig{
				PathRegex: `^/api/(?P<path>.*)$`,
			},
			Upstream: config.UpstreamConfig{
				URL:     upstream.URL,
				Timeout: 5_000_000_000,
			},
		},
	}

	handlers := make(map[string]http.Handler, len(routes))
	for i := range routes {
		handlers[routes[i].Name] = proxy.NewProxyHandler(&routes[i])
	}

	r := router.New(routes, handlers)

	chain := middleware.Chain(
		middleware.Recovery(),
		middleware.RequestID(),
	)

	mux := http.NewServeMux()
	mux.Handle("/", chain(r))
	return mux
}

func TestIntegration_ProxyWithPathRewrite(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gw := setupGateway(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Upstream-Path"); got != "/internal/users/42" {
		t.Errorf("expected upstream path /internal/users/42, got %s", got)
	}
}

func TestIntegration_RequestIDPropagation(t *testing.T) {
	var upstreamRequestID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestID = r.Header.Get("X-Request-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gw := setupGateway(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/api/users/1", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	responseID := rr.Header().Get("X-Request-ID")
	if responseID == "" {
		t.Fatal("expected X-Request-ID in response")
	}
	if upstreamRequestID == "" {
		t.Fatal("expected X-Request-ID forwarded to upstream")
	}
	if responseID != upstreamRequestID {
		t.Errorf("response ID %q != upstream ID %q", responseID, upstreamRequestID)
	}
}

func TestIntegration_404OnUnknownPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gw := setupGateway(t, upstream)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %s", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if body["error"] != "not_found" {
		t.Errorf("expected error=not_found, got %s", body["error"])
	}
}

func TestIntegration_MethodFiltering(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gw := setupGateway(t, upstream)

	// DELETE on user-service (only GET/POST allowed) should fall through to catch-all
	req := httptest.NewRequest(http.MethodDelete, "/api/users/42", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	// Falls through to catch-all route, so should be 200
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (catch-all), got %d", rr.Code)
	}
}

func TestIntegration_PanicRecovery(t *testing.T) {
	// Build a gateway where a route handler panics
	routes := []config.RouteConfig{
		{
			Name: "panic-route",
			Match: config.MatchConfig{
				PathRegex: `^/panic$`,
			},
			Upstream: config.UpstreamConfig{
				URL:     "http://127.0.0.1:1",
				Timeout: 1_000_000_000,
			},
		},
	}

	// Use a panicking handler instead of the proxy
	panicHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic in integration")
	})

	handlers := map[string]http.Handler{
		"panic-route": panicHandler,
	}

	r := router.New(routes, handlers)

	chain := middleware.Chain(
		middleware.Recovery(),
		middleware.RequestID(),
	)

	mux := http.NewServeMux()
	mux.Handle("/", chain(r))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if body["error"] != "internal_error" {
		t.Errorf("expected error=internal_error, got %s", body["error"])
	}
}
