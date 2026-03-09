package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// serveJWKS creates a test server returning JWKS for the given RSA public key.
func serveJWKS(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	jwks := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": kid,
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
}

func TestOIDC_JWKSFetch(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := serveJWKS(t, "test-kid", &privKey.PublicKey)
	defer srv.Close()

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		JWKSURL:  srv.URL,
		CacheTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	// Verify key was cached.
	key := a.cache.getKey("test-kid")
	if key == nil {
		t.Fatal("expected key to be cached")
	}
}

func TestOIDC_ValidToken(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := serveJWKS(t, "kid-1", &privKey.PublicKey)
	defer srv.Close()

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		JWKSURL:  srv.URL,
		CacheTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "oidc-user",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	token.Header["kid"] = "kid-1"
	tokenStr, err := token.SignedString(privKey)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	result, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Subject != "oidc-user" {
		t.Fatalf("expected subject oidc-user, got %s", result.Subject)
	}
}

func TestOIDC_UnknownKid(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := serveJWKS(t, "kid-1", &privKey.PublicKey)
	defer srv.Close()

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		JWKSURL:  srv.URL,
		CacheTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "oidc-user",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	token.Header["kid"] = "unknown-kid"
	tokenStr, _ := token.SignedString(privKey)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for unknown kid")
	}
}

func TestOIDC_Introspection(t *testing.T) {
	introspectionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		tok := r.FormValue("token")
		resp := map[string]any{"active": tok == "valid-token", "sub": "intro-user", "client_id": "my-client"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer introspectionSrv.Close()

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IntrospectionURL: introspectionSrv.URL,
		ClientID:         "client-1",
		ClientSecret:     "secret-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer valid-token")

	result, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Subject != "intro-user" {
		t.Fatalf("expected subject intro-user, got %s", result.Subject)
	}
}

func TestOIDC_IntrospectionInactive(t *testing.T) {
	introspectionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"active": false}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer introspectionSrv.Close()

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IntrospectionURL: introspectionSrv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer some-token")

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for inactive token")
	}
	if err.Error() != "token is not active" {
		t.Fatalf("expected 'token is not active', got %q", err.Error())
	}
}

func TestOIDC_Discovery(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	jwksSrv := serveJWKS(t, "disc-kid", &privKey.PublicKey)
	defer jwksSrv.Close()

	// Discovery server returns jwks_uri pointing to jwksSrv.
	discoverySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"jwks_uri": jwksSrv.URL,
		})
	}))
	defer discoverySrv.Close()

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IssuerURL: discoverySrv.URL,
		CacheTTL:  time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	key := a.cache.getKey("disc-kid")
	if key == nil {
		t.Fatal("expected key from discovery to be cached")
	}
}

func TestOIDC_MissingAuthHeader(t *testing.T) {
	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IntrospectionURL: "http://unused",
	})
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for missing header")
	}
	if err.Error() != "missing authorization header" {
		t.Fatalf("expected 'missing authorization header', got %q", err.Error())
	}
}
