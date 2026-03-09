package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
)

func TestRouter_BasicMatch(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "test-route",
			Match: config.MatchConfig{
				PathRegex: `^/api/test$`,
				Methods:   []string{"GET"},
			},
		},
	}

	called := false
	handlers := map[string]http.Handler{
		"test-route": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.Header().Set("X-Test", "matched")
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if !called {
		t.Fatal("expected handler to be called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Test"); got != "matched" {
		t.Fatalf("expected X-Test header to be 'matched', got %q", got)
	}
}

func TestRouter_NamedCaptureGroups(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "user-by-id",
			Match: config.MatchConfig{
				PathRegex: `^/api/users/(?P<id>[^/]+)$`,
			},
		},
	}

	var capturedID string
	handlers := map[string]http.Handler{
		"user-by-id": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mr := RouteFromContext(r.Context())
			if mr == nil {
				t.Fatal("expected MatchedRoute in context, got nil")
				return
			}
			capturedID = mr.PathParams["id"]
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if capturedID != "42" {
		t.Fatalf("expected PathParams[\"id\"] to be \"42\", got %q", capturedID)
	}
}

func TestRouter_MultipleCaptureGroups(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "resource-by-id",
			Match: config.MatchConfig{
				PathRegex: `^/api/(?P<resource>[^/]+)/(?P<id>[^/]+)$`,
			},
		},
	}

	var capturedResource, capturedID string
	handlers := map[string]http.Handler{
		"resource-by-id": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mr := RouteFromContext(r.Context())
			if mr == nil {
				t.Fatal("expected MatchedRoute in context, got nil")
				return
			}
			capturedResource = mr.PathParams["resource"]
			capturedID = mr.PathParams["id"]
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/orders/123", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if capturedResource != "orders" {
		t.Fatalf("expected PathParams[\"resource\"] to be \"orders\", got %q", capturedResource)
	}
	if capturedID != "123" {
		t.Fatalf("expected PathParams[\"id\"] to be \"123\", got %q", capturedID)
	}
}

func TestRouter_FirstMatchWins(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "special-user",
			Match: config.MatchConfig{
				PathRegex: `^/api/users/special$`,
			},
		},
		{
			Name: "user-by-id",
			Match: config.MatchConfig{
				PathRegex: `^/api/users/(?P<id>[^/]+)$`,
			},
		},
	}

	handlers := map[string]http.Handler{
		"special-user": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		"user-by-id": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/users/special", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected first handler (200), got %d", rr.Code)
	}
}

func TestRouter_MethodFiltering(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "data-route",
			Match: config.MatchConfig{
				PathRegex: `^/api/data$`,
				Methods:   []string{"GET", "POST"},
			},
		},
	}

	handlers := map[string]http.Handler{
		"data-route": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodDelete, "/api/data", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disallowed method, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if body["error"] != "not_found" {
		t.Fatalf("expected error \"not_found\", got %q", body["error"])
	}
}

func TestRouter_AllMethodsAllowed(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "open-route",
			Match: config.MatchConfig{
				PathRegex: `^/api/open$`,
				Methods:   nil,
			},
		},
	}

	called := false
	handlers := map[string]http.Handler{
		"open-route": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodDelete, "/api/open", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if !called {
		t.Fatal("expected handler to be called for DELETE when methods is nil")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestRouter_NoMatch_404(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "test-route",
			Match: config.MatchConfig{
				PathRegex: `^/api/test$`,
			},
		},
	}

	handlers := map[string]http.Handler{
		"test-route": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if body["error"] != "not_found" {
		t.Fatalf("expected error \"not_found\", got %q", body["error"])
	}
}

func TestRouter_ContextContainsMatchedRoute(t *testing.T) {
	configs := []config.RouteConfig{
		{
			Name: "context-route",
			Match: config.MatchConfig{
				PathRegex: `^/api/ctx/(?P<key>[^/]+)$`,
			},
		},
	}

	var matchedRoute *MatchedRoute
	handlers := map[string]http.Handler{
		"context-route": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			matchedRoute = RouteFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	}

	router := New(configs, handlers)

	req := httptest.NewRequest(http.MethodGet, "/api/ctx/abc", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if matchedRoute == nil {
		t.Fatal("expected MatchedRoute in context, got nil")
	}
	if matchedRoute.Config == nil {
		t.Fatal("expected Config pointer to be non-nil")
	}
	if matchedRoute.Config.Name != "context-route" {
		t.Fatalf("expected Config.Name \"context-route\", got %q", matchedRoute.Config.Name)
	}
	if matchedRoute.PathParams == nil {
		t.Fatal("expected PathParams to be non-nil")
	}
	if matchedRoute.PathParams["key"] != "abc" {
		t.Fatalf("expected PathParams[\"key\"] to be \"abc\", got %q", matchedRoute.PathParams["key"])
	}
}

func TestRouteFromContext_NilWhenMissing(t *testing.T) {
	mr := RouteFromContext(context.Background())
	if mr != nil {
		t.Fatalf("expected nil MatchedRoute from empty context, got %+v", mr)
	}
}
