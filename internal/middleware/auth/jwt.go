package auth

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// JWTAuthenticator validates JWT bearer tokens.
type JWTAuthenticator struct {
	signingKey any
	algorithm  string
	issuer     string
	audience   string
}

// NewJWTAuthenticator builds a JWTAuthenticator. It parses key material once
// at construction time so per-request overhead is minimal.
func NewJWTAuthenticator(cfg *config.JWTConfig) (*JWTAuthenticator, error) {
	a := &JWTAuthenticator{
		algorithm: cfg.Algorithm,
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
	}

	switch {
	case strings.HasPrefix(cfg.Algorithm, "HS"):
		if cfg.Secret == "" {
			return nil, errors.New("jwt: HMAC algorithm requires secret")
		}
		a.signingKey = []byte(cfg.Secret)

	case strings.HasPrefix(cfg.Algorithm, "RS"):
		key, err := loadRSAPublicKey(cfg.PublicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("jwt: %w", err)
		}
		a.signingKey = key

	case strings.HasPrefix(cfg.Algorithm, "ES"):
		key, err := loadECDSAPublicKey(cfg.PublicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("jwt: %w", err)
		}
		a.signingKey = key

	default:
		return nil, fmt.Errorf("jwt: unsupported algorithm %q", cfg.Algorithm)
	}

	return a, nil
}

// Authenticate extracts a Bearer token from the Authorization header and
// validates it against the configured signing key, algorithm, issuer, and
// audience.
func (a *JWTAuthenticator) Authenticate(r *http.Request) (*AuthResult, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, errors.New("missing authorization header")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, errors.New("malformed token")
	}
	tokenString := parts[1]

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{a.algorithm}),
	}
	if a.issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(a.issuer))
	}
	if a.audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(a.audience))
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		return a.keyFunc(t)
	}, parserOpts...)

	if err != nil {
		return nil, classifyJWTError(err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	claimsMap := make(map[string]any, len(claims))
	for k, v := range claims {
		claimsMap[k] = v
	}

	subject, _ := claims.GetSubject()

	return &AuthResult{
		Subject: subject,
		Claims:  claimsMap,
	}, nil
}

func (a *JWTAuthenticator) keyFunc(t *jwt.Token) (any, error) {
	switch a.signingKey.(type) {
	case []byte:
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
	case *rsa.PublicKey:
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
	case *ecdsa.PublicKey:
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
	}
	return a.signingKey, nil
}

func classifyJWTError(err error) error {
	if errors.Is(err, jwt.ErrTokenExpired) {
		return errors.New("token expired")
	}
	if errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		return errors.New("invalid token signature")
	}
	return fmt.Errorf("invalid token: %v", err)
}

func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading public key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		// Try PKCS1 format as fallback.
		rsaKey, err2 := x509.ParsePKCS1PublicKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parsing public key: %w", err)
		}
		return rsaKey, nil
	}

	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not RSA")
	}
	return rsaKey, nil
}

func loadECDSAPublicKey(path string) (*ecdsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading public key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing public key: %w", err)
	}

	ecKey, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not ECDSA")
	}
	return ecKey, nil
}
