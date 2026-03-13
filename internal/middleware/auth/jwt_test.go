package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

const testHMACSecret = "super-secret-key-for-testing-purposes"

func makeHMACToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := token.SignedString([]byte(testHMACSecret))
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return s
}

func TestJWT_ValidHS256(t *testing.T) {
	a, err := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
	})
	if err != nil {
		t.Fatal(err)
	}

	tokenStr := makeHMACToken(t, jwt.MapClaims{
		"sub": "user-42",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	result, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Subject != "user-42" {
		t.Fatalf("expected subject user-42, got %s", result.Subject)
	}
}

func TestJWT_ExpiredToken(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
	})

	tokenStr := makeHMACToken(t, jwt.MapClaims{
		"sub": "user-42",
		"exp": jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if err.Error() != "token expired" {
		t.Fatalf("expected 'token expired', got %q", err.Error())
	}
}

func TestJWT_WrongSignature(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
	})

	// Sign with a different secret.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-42",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	tokenStr, _ := token.SignedString([]byte("wrong-secret"))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for wrong signature")
	}
	if err.Error() != "invalid token signature" {
		t.Fatalf("expected 'invalid token signature', got %q", err.Error())
	}
}

func TestJWT_IssuerMismatch(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
		Issuer:    "expected-issuer",
	})

	tokenStr := makeHMACToken(t, jwt.MapClaims{
		"sub": "user-42",
		"iss": "wrong-issuer",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for issuer mismatch")
	}
}

func TestJWT_AudienceMismatch(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
		Audience:  "expected-audience",
	})

	tokenStr := makeHMACToken(t, jwt.MapClaims{
		"sub": "user-42",
		"aud": "wrong-audience",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for audience mismatch")
	}
}

func TestJWT_MissingBearerToken(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for missing header")
	}
	if err.Error() != "missing authorization header" {
		t.Fatalf("expected 'missing authorization header', got %q", err.Error())
	}
}

func TestJWT_MalformedHeader(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "NotBearer xyz")

	_, err := a.Authenticate(r)
	if err == nil {
		t.Fatal("expected error for malformed header")
	}
	if err.Error() != "malformed token" {
		t.Fatalf("expected 'malformed token', got %q", err.Error())
	}
}

func TestJWT_ValidRS256(t *testing.T) {
	// Generate RSA key pair.
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	// Write public key to temp file.
	pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	tmpDir := t.TempDir()
	pubFile := filepath.Join(tmpDir, "rsa_pub.pem")
	if err := os.WriteFile(pubFile, pubPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := NewJWTAuthenticator(&config.JWTConfig{
		PublicKeyFile: pubFile,
		Algorithm:     "RS256",
	})
	if err != nil {
		t.Fatal(err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "rsa-user",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
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
	if result.Subject != "rsa-user" {
		t.Fatalf("expected subject rsa-user, got %s", result.Subject)
	}
}

func TestJWT_ValidES256(t *testing.T) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	tmpDir := t.TempDir()
	pubFile := filepath.Join(tmpDir, "ec_pub.pem")
	if err := os.WriteFile(pubFile, pubPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := NewJWTAuthenticator(&config.JWTConfig{
		PublicKeyFile: pubFile,
		Algorithm:     "ES256",
	})
	if err != nil {
		t.Fatal(err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub": "ec-user",
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
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
	if result.Subject != "ec-user" {
		t.Fatalf("expected subject ec-user, got %s", result.Subject)
	}
}

func TestJWT_ClaimsInResult(t *testing.T) {
	a, _ := NewJWTAuthenticator(&config.JWTConfig{
		Secret:    testHMACSecret,
		Algorithm: "HS256",
	})

	tokenStr := makeHMACToken(t, jwt.MapClaims{
		"sub":  "user-42",
		"role": "admin",
		"exp":  jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenStr)

	result, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	if result.Claims["role"] != "admin" {
		t.Fatalf("expected role=admin in claims, got %v", result.Claims["role"])
	}
	if result.Claims["sub"] != "user-42" {
		t.Fatalf("expected sub=user-42 in claims, got %v", result.Claims["sub"])
	}
}
