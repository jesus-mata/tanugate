package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

func TestNewAuthenticator_JWT(t *testing.T) {
	provider := config.AuthProvider{
		Type: "jwt",
		JWT: &config.JWTConfig{
			Secret:    "test-secret",
			Algorithm: "HS256",
		},
	}
	a, err := NewAuthenticator(provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := a.(*JWTAuthenticator); !ok {
		t.Fatalf("expected *JWTAuthenticator, got %T", a)
	}
}

func TestNewAuthenticator_APIKey(t *testing.T) {
	provider := config.AuthProvider{
		Type: "apikey",
		APIKey: &config.APIKeyConfig{
			Header: "X-API-Key",
			Keys: []config.APIKeyEntry{
				{Key: "abc123", Name: "test"},
			},
		},
	}
	a, err := NewAuthenticator(provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := a.(*APIKeyAuthenticator); !ok {
		t.Fatalf("expected *APIKeyAuthenticator, got %T", a)
	}
}

func TestNewAuthenticator_Unknown(t *testing.T) {
	provider := config.AuthProvider{Type: "magic"}
	_, err := NewAuthenticator(provider)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestMiddleware_SkipsNone(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := Middleware(nil)
	handler := mw(next)

	// Route with auth provider "none"
	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"none"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler was not called")
	}
}

func TestMiddleware_SkipsNilAuth(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	mw := Middleware(nil)
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler was not called for nil auth config")
	}
}

func TestMiddleware_StoresAuthResult(t *testing.T) {
	mockAuth := &mockAuthenticator{
		result: &AuthResult{Subject: "user-1", Claims: map[string]any{"role": "admin"}},
	}

	var captured *AuthResult
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = ResultFromContext(r.Context())
	})

	mw := Middleware(map[string]Authenticator{"mock": mockAuth})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"mock"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if captured == nil {
		t.Fatal("AuthResult not stored in context")
	}
	if captured.Subject != "user-1" {
		t.Fatalf("expected subject user-1, got %s", captured.Subject)
	}
	if captured.Provider != "mock" {
		t.Fatalf("expected provider mock, got %s", captured.Provider)
	}
}

func TestMiddleware_Returns401(t *testing.T) {
	mockAuth := &mockAuthenticator{err: errUnauthorized}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})

	mw := Middleware(map[string]Authenticator{"mock": mockAuth})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"mock"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["message"] != "authentication failed" {
		t.Fatalf("expected message=authentication failed, got %s", body["message"])
	}
}

func TestMiddleware_MissingProvider(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})

	// Empty authenticators map — provider "missing" doesn't exist.
	mw := Middleware(map[string]Authenticator{})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"missing"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestContextHelpers(t *testing.T) {
	ar := &AuthResult{Subject: "ctx-test"}
	ctx := WithAuthResult(context.Background(), ar)

	got := ResultFromContext(ctx)
	if got == nil || got.Subject != "ctx-test" {
		t.Fatal("context round-trip failed")
	}

	if ResultFromContext(context.Background()) != nil {
		t.Fatal("expected nil from empty context")
	}
}

// --- helpers ---

type mockAuthenticator struct {
	result *AuthResult
	err    error
	called bool
}

var errUnauthorized = &authError{msg: "unauthorized"}

type authError struct{ msg string }

func (e *authError) Error() string { return e.msg }

func (m *mockAuthenticator) Authenticate(_ *http.Request) (*AuthResult, error) {
	m.called = true
	return m.result, m.err
}

// --- Multiple providers tests ---

func TestMiddleware_MultipleProviders_FirstSucceeds(t *testing.T) {
	first := &mockAuthenticator{result: &AuthResult{Subject: "user-1"}}
	second := &mockAuthenticator{result: &AuthResult{Subject: "user-2"}}

	var captured *AuthResult
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = ResultFromContext(r.Context())
	})

	mw := Middleware(map[string]Authenticator{"first": first, "second": second})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"first", "second"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if captured == nil || captured.Subject != "user-1" {
		t.Fatal("expected first provider to authenticate")
	}
	if captured.Provider != "first" {
		t.Fatalf("expected Provider=first, got %s", captured.Provider)
	}
	if second.called {
		t.Fatal("second provider should not have been called")
	}
}

func TestMiddleware_MultipleProviders_SecondSucceeds(t *testing.T) {
	first := &mockAuthenticator{err: errUnauthorized}
	second := &mockAuthenticator{result: &AuthResult{Subject: "user-2"}}

	var captured *AuthResult
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = ResultFromContext(r.Context())
	})

	mw := Middleware(map[string]Authenticator{"first": first, "second": second})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"first", "second"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if captured == nil || captured.Subject != "user-2" {
		t.Fatal("expected second provider to authenticate")
	}
	if captured.Provider != "second" {
		t.Fatalf("expected Provider=second, got %s", captured.Provider)
	}
}

func TestMiddleware_MultipleProviders_AllFail(t *testing.T) {
	first := &mockAuthenticator{err: errUnauthorized}
	second := &mockAuthenticator{err: errUnauthorized}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})

	mw := Middleware(map[string]Authenticator{"first": first, "second": second})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"first", "second"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["message"] != "authentication failed" {
		t.Fatalf("expected message=authentication failed, got %s", body["message"])
	}
}

func TestMiddleware_MultipleProviders_MisconfiguredInMiddle(t *testing.T) {
	valid := &mockAuthenticator{err: errUnauthorized}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})

	mw := Middleware(map[string]Authenticator{"valid": valid})
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{"valid", "missing"}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestMiddleware_EmptyProvidersList(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	mw := Middleware(nil)
	handler := mw(next)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := router.WithMatchedRoute(r.Context(), &router.MatchedRoute{
		Config: &config.RouteConfig{
			Auth: &config.RouteAuthConfig{Providers: []string{}},
		},
	})
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Fatal("next handler was not called for empty providers list")
	}
}
