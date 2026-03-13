package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jesus-mata/tanugate/internal/config"
)

func newAPIKeyAuth(keys ...config.APIKeyEntry) *APIKeyAuthenticator {
	return NewAPIKeyAuthenticator(&config.APIKeyConfig{
		Header: "X-API-Key",
		Keys:   keys,
	})
}

func TestAPIKey_ValidKey(t *testing.T) {
	a := newAPIKeyAuth(config.APIKeyEntry{Key: "key-abc", Name: "service-a"})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-API-Key", "key-abc")

	result, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != "service-a" {
		t.Fatalf("expected name service-a, got %s", result.Name)
	}
	if result.Subject != "service-a" {
		t.Fatalf("expected subject service-a, got %s", result.Subject)
	}
}

func TestAPIKey_InvalidKey(t *testing.T) {
	a := newAPIKeyAuth(config.APIKeyEntry{Key: "key-abc", Name: "service-a"})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-API-Key", "wrong-key")

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
	if err.Error() != "invalid API key" {
		t.Fatalf("expected 'invalid API key', got %q", err.Error())
	}
}

func TestAPIKey_MissingHeader(t *testing.T) {
	a := newAPIKeyAuth(config.APIKeyEntry{Key: "key-abc", Name: "service-a"})

	r := httptest.NewRequest(http.MethodGet, "/", nil)

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for missing header")
	}
	if err.Error() != "missing API key" {
		t.Fatalf("expected 'missing API key', got %q", err.Error())
	}
}

func TestAPIKey_MultipleKeys(t *testing.T) {
	a := newAPIKeyAuth(
		config.APIKeyEntry{Key: "key-first", Name: "first"},
		config.APIKeyEntry{Key: "key-second", Name: "second"},
		config.APIKeyEntry{Key: "key-third", Name: "third"},
	)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-API-Key", "key-second")

	result, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != "second" {
		t.Fatalf("expected name second, got %s", result.Name)
	}
}

func TestAPIKey_DefaultHeader(t *testing.T) {
	a := NewAPIKeyAuthenticator(&config.APIKeyConfig{
		Keys: []config.APIKeyEntry{{Key: "k1", Name: "n1"}},
	})

	if a.header != "X-API-Key" {
		t.Fatalf("expected default header X-API-Key, got %s", a.header)
	}
}
