package pipeline

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------------------
// A. Registry tests
// ---------------------------------------------------------------------------

func TestRegistry_BuildUnknownType(t *testing.T) {
	r := NewRegistry()
	_, err := r.Build("bogus", &FactoryDeps{}, "route", "mw", nil)
	if err == nil {
		t.Fatal("expected error for unknown middleware type, got nil")
	}
	if got := err.Error(); !contains(got, "unknown middleware type") {
		t.Fatalf("expected error to contain %q, got %q", "unknown middleware type", got)
	}
}

func TestRegistry_RegisterAndBuild(t *testing.T) {
	r := NewRegistry()

	called := false
	r.Register("mock", func(_ *FactoryDeps, _, _ string, _ any) (middleware.Middleware, error) {
		called = true
		return func(next http.Handler) http.Handler { return next }, nil
	})

	mw, err := r.Build("mock", &FactoryDeps{}, "route", "my-mock", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mw == nil {
		t.Fatal("expected non-nil middleware")
	}
	if !called {
		t.Fatal("expected factory to be called")
	}
}

func TestDefaultRegistry_HasAllTypes(t *testing.T) {
	r := DefaultRegistry()
	for _, typ := range []string{"cors", "rate_limit", "auth"} {
		if _, ok := r.factories[typ]; !ok {
			t.Errorf("DefaultRegistry missing factory for type %q", typ)
		}
	}
}

// ---------------------------------------------------------------------------
// B. filterSkipped tests (table-driven)
// ---------------------------------------------------------------------------

func TestFilterSkipped(t *testing.T) {
	mw := func(name, typ string) config.ResolvedMiddleware {
		return config.ResolvedMiddleware{Name: name, Type: typ}
	}

	tests := []struct {
		name      string
		input     []config.ResolvedMiddleware
		skip      []string
		wantCount int
	}{
		{
			name:      "empty skip list",
			input:     []config.ResolvedMiddleware{mw("a", "auth"), mw("b", "cors"), mw("c", "rate_limit")},
			skip:      nil,
			wantCount: 3,
		},
		{
			name:      "skip all",
			input:     []config.ResolvedMiddleware{mw("a", "auth"), mw("b", "cors"), mw("c", "rate_limit")},
			skip:      []string{"a", "b", "c"},
			wantCount: 0,
		},
		{
			name:      "skip partial",
			input:     []config.ResolvedMiddleware{mw("a", "auth"), mw("b", "cors"), mw("c", "rate_limit")},
			skip:      []string{"b"},
			wantCount: 2,
		},
		{
			name:      "skip nonexistent",
			input:     []config.ResolvedMiddleware{mw("a", "auth"), mw("b", "cors"), mw("c", "rate_limit")},
			skip:      []string{"zzz"},
			wantCount: 3,
		},
		{
			name:      "empty input",
			input:     nil,
			skip:      []string{"a"},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterSkipped(tt.input, tt.skip)
			if len(got) != tt.wantCount {
				t.Errorf("filterSkipped returned %d items, want %d", len(got), tt.wantCount)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// C. wrapResilience tests (table-driven)
// ---------------------------------------------------------------------------

func TestWrapResilience(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	promReg := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promReg)

	tests := []struct {
		name  string
		route config.RouteConfig
	}{
		{
			name: "nil CB nil Retry",
			route: config.RouteConfig{
				Name:           "test-route",
				CircuitBreaker: nil,
				Retry:          nil,
			},
		},
		{
			name: "CB only",
			route: config.RouteConfig{
				Name: "test-route",
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 5,
					SuccessThreshold: 3,
					Timeout:          30 * time.Second,
				},
				Retry: nil,
			},
		},
		{
			name: "Retry only",
			route: config.RouteConfig{
				Name:           "test-route",
				CircuitBreaker: nil,
				Retry: &config.RetryConfig{
					MaxRetries:           1,
					InitialDelay:         1 * time.Millisecond,
					Multiplier:           1.0,
					RetryableStatusCodes: []int{502},
				},
			},
		},
		{
			name: "both CB and Retry",
			route: config.RouteConfig{
				Name: "test-route",
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 5,
					SuccessThreshold: 3,
					Timeout:          30 * time.Second,
				},
				Retry: &config.RetryConfig{
					MaxRetries:           1,
					InitialDelay:         1 * time.Millisecond,
					Multiplier:           1.0,
					RetryableStatusCodes: []int{502},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := wrapResilience(&tt.route, inner, metrics)
			if h == nil {
				t.Fatal("wrapResilience returned nil handler")
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", rec.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// D. BuildHandler tests
// ---------------------------------------------------------------------------

func TestBuildHandler_MinimalHappyPath(t *testing.T) {
	// Spin up a backend that returns 200 with a JSON body.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	cfg := &config.GatewayConfig{
		MiddlewareDefinitions: map[string]config.MiddlewareDefinition{},
		Routes: []config.RouteConfig{
			{
				Name: "test-route",
				Match: config.MatchConfig{
					PathRegex: `^/test`,
				},
				Upstream: config.UpstreamConfig{
					URL:     backend.URL,
					Timeout: 5 * time.Second,
				},
			},
		},
	}

	promReg := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promReg)
	deps := &FactoryDeps{
		Authenticators: make(map[string]auth.Authenticator),
		Metrics:        metrics,
		Logger:         slog.Default(),
	}

	h, cleanup, err := BuildHandler(cfg, deps, DefaultRegistry())
	if err != nil {
		t.Fatalf("BuildHandler failed: %v", err)
	}
	t.Cleanup(cleanup)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestBuildHandler_InvalidMiddlewareRef(t *testing.T) {
	cfg := &config.GatewayConfig{
		MiddlewareDefinitions: map[string]config.MiddlewareDefinition{},
		Routes: []config.RouteConfig{
			{
				Name: "test-route",
				Match: config.MatchConfig{
					PathRegex: `^/test`,
				},
				Upstream: config.UpstreamConfig{
					URL:     "http://localhost:9999",
					Timeout: 5 * time.Second,
				},
				Middlewares: []config.MiddlewareRef{
					{Ref: "nonexistent"},
				},
			},
		},
	}

	promReg := prometheus.NewRegistry()
	metrics := observability.NewMetricsCollector(promReg)
	deps := &FactoryDeps{
		Metrics: metrics,
		Logger:  slog.Default(),
	}

	_, cleanup, err := BuildHandler(cfg, deps, DefaultRegistry())
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err == nil {
		t.Fatal("expected error for invalid middleware ref, got nil")
	}
	if got := err.Error(); !contains(got, "nonexistent") {
		t.Fatalf("expected error to contain %q, got %q", "nonexistent", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
