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

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// setupOIDCTestServer creates a test server that serves both the OIDC discovery
// document and the JWKS endpoint. The discovery document's issuer field is set
// to the server's own URL so go-oidc's issuer validation passes.
func setupOIDCTestServer(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"jwks_uri":                              srv.URL + "/jwks",
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"id_token"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
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
		})
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// serveJWKSOnly creates a test server that only serves a JWKS endpoint (no discovery).
func serveJWKSOnly(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signToken creates a signed JWT with the given claims and key ID.
func signToken(t *testing.T, privKey *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	tokenStr, err := token.SignedString(privKey)
	if err != nil {
		t.Fatal(err)
	}
	return tokenStr
}

func TestOIDC_Discovery_ValidToken(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := setupOIDCTestServer(t, "kid-1", &privKey.PublicKey)

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IssuerURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	tokenStr := signToken(t, privKey, "kid-1", jwt.MapClaims{
		"sub": "oidc-user",
		"iss": srv.URL,
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})

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

func TestOIDC_JWKS_ValidToken(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := serveJWKSOnly(t, "kid-1", &privKey.PublicKey)

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		JWKSURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	tokenStr := signToken(t, privKey, "kid-1", jwt.MapClaims{
		"sub": "oidc-user",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})

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

	srv := serveJWKSOnly(t, "kid-1", &privKey.PublicKey)

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		JWKSURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	tokenStr := signToken(t, privKey, "unknown-kid", jwt.MapClaims{
		"sub": "oidc-user",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for unknown kid")
	}
}

func TestOIDC_IssuerMismatch(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := setupOIDCTestServer(t, "kid-1", &privKey.PublicKey)

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IssuerURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	// Token has a different issuer than the discovery server.
	tokenStr := signToken(t, privKey, "kid-1", jwt.MapClaims{
		"sub": "oidc-user",
		"iss": "https://evil.example.com",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for issuer mismatch")
	}
	if err.Error() != "invalid token issuer" {
		t.Fatalf("expected 'invalid token issuer', got %q", err.Error())
	}
}

func TestOIDC_AudienceValidation(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := setupOIDCTestServer(t, "kid-1", &privKey.PublicKey)

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IssuerURL: srv.URL,
		Audience:  "my-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	// Token has a different audience.
	tokenStr := signToken(t, privKey, "kid-1", jwt.MapClaims{
		"sub": "oidc-user",
		"iss": srv.URL,
		"aud": "wrong-audience",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for wrong audience")
	}
	if err.Error() != "invalid token audience" {
		t.Fatalf("expected 'invalid token audience', got %q", err.Error())
	}
}

func TestOIDC_AlgorithmRestriction(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := setupOIDCTestServer(t, "kid-1", &privKey.PublicKey)

	// Only allow ES256, but token is signed with RS256.
	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IssuerURL:         srv.URL,
		AllowedAlgorithms: []string{"ES256"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	tokenStr := signToken(t, privKey, "kid-1", jwt.MapClaims{
		"sub": "oidc-user",
		"iss": srv.URL,
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for algorithm mismatch")
	}
}

func TestOIDC_ExpiredToken(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := setupOIDCTestServer(t, "kid-1", &privKey.PublicKey)

	a, err := NewOIDCAuthenticator(&config.OIDCConfig{
		IssuerURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	tokenStr := signToken(t, privKey, "kid-1", jwt.MapClaims{
		"sub": "oidc-user",
		"iss": srv.URL,
		"exp": jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		"iat": jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err = a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if err.Error() != "token expired" {
		t.Fatalf("expected 'token expired', got %q", err.Error())
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
