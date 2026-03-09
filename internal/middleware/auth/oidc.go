package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// OIDCAuthenticator validates tokens using OIDC JWKS or token introspection.
type OIDCAuthenticator struct {
	cache            *jwksCache
	audience         string
	introspectionURL string
	clientID         string
	clientSecret     string
	httpClient       *http.Client
}

type jwksCache struct {
	mu       sync.RWMutex
	keys     map[string]any // kid → public key
	expiry   time.Time
	ttl      time.Duration
	fetchURL string
	client   *http.Client
	cancel   context.CancelFunc
}

type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type discoveryDoc struct {
	JWKSURI string `json:"jwks_uri"`
}

type introspectionResponse struct {
	Active   bool   `json:"active"`
	Sub      string `json:"sub"`
	ClientID string `json:"client_id"`
}

// NewOIDCAuthenticator creates an OIDC authenticator. It resolves the JWKS URL
// (via discovery if needed), fetches the initial key set, and starts a
// background refresh goroutine.
func NewOIDCAuthenticator(cfg *config.OIDCConfig) (*OIDCAuthenticator, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}

	a := &OIDCAuthenticator{
		audience:         cfg.Audience,
		introspectionURL: cfg.IntrospectionURL,
		clientID:         cfg.ClientID,
		clientSecret:     cfg.ClientSecret,
		httpClient:       httpClient,
	}

	// If only introspection is configured, no JWKS needed.
	if cfg.IntrospectionURL != "" && cfg.JWKSURL == "" && cfg.IssuerURL == "" {
		return a, nil
	}

	jwksURL := cfg.JWKSURL
	if jwksURL == "" && cfg.IssuerURL != "" {
		discovered, err := discoverJWKSURL(httpClient, cfg.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("oidc discovery: %w", err)
		}
		jwksURL = discovered
	}

	if jwksURL == "" {
		return nil, errors.New("oidc: either jwks_url, issuer_url, or introspection_url is required")
	}

	ttl := cfg.CacheTTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}

	ctx, cancel := context.WithCancel(context.Background())
	cache := &jwksCache{
		ttl:      ttl,
		fetchURL: jwksURL,
		client:   httpClient,
		cancel:   cancel,
	}

	if err := cache.refresh(); err != nil {
		cancel()
		return nil, fmt.Errorf("oidc: initial JWKS fetch: %w", err)
	}

	go cache.backgroundRefresh(ctx)

	a.cache = cache
	return a, nil
}

// Authenticate validates the token, preferring introspection if configured,
// otherwise falling back to local JWKS validation.
func (a *OIDCAuthenticator) Authenticate(r *http.Request) (*AuthResult, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, errors.New("missing authorization header")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, errors.New("malformed token")
	}
	tokenString := parts[1]

	if a.introspectionURL != "" {
		return a.introspect(tokenString)
	}

	return a.validateJWKS(tokenString)
}

// Stop cancels the background JWKS refresh goroutine.
func (a *OIDCAuthenticator) Stop() {
	if a.cache != nil && a.cache.cancel != nil {
		a.cache.cancel()
	}
}

func (a *OIDCAuthenticator) validateJWKS(tokenString string) (*AuthResult, error) {
	parserOpts := []jwt.ParserOption{}
	if a.audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(a.audience))
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, errors.New("token missing kid header")
		}
		key := a.cache.getKey(kid)
		if key == nil {
			return nil, fmt.Errorf("unknown kid: %s", kid)
		}
		return key, nil
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

func (a *OIDCAuthenticator) introspect(tokenString string) (*AuthResult, error) {
	body := strings.NewReader("token=" + tokenString)
	req, err := http.NewRequest(http.MethodPost, a.introspectionURL, body)
	if err != nil {
		return nil, fmt.Errorf("introspection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if a.clientID != "" && a.clientSecret != "" {
		req.SetBasicAuth(a.clientID, a.clientSecret)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspection request failed: %w", err)
	}
	defer resp.Body.Close()

	var result introspectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("introspection decode: %w", err)
	}

	if !result.Active {
		return nil, errors.New("token is not active")
	}

	return &AuthResult{
		Subject: result.Sub,
		Claims: map[string]any{
			"sub":       result.Sub,
			"client_id": result.ClientID,
			"active":    true,
		},
	}, nil
}

func (c *jwksCache) getKey(kid string) any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[kid]
}

func (c *jwksCache) refresh() error {
	resp, err := c.client.Get(c.fetchURL)
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decoding JWKS: %w", err)
	}

	keys := make(map[string]any, len(jwks.Keys))
	for _, k := range jwks.Keys {
		pub, err := parseJWK(k)
		if err != nil {
			continue // skip keys we can't parse
		}
		keys[k.Kid] = pub
	}

	c.mu.Lock()
	c.keys = keys
	c.expiry = time.Now().Add(c.ttl)
	c.mu.Unlock()

	return nil
}

func (c *jwksCache) backgroundRefresh(ctx context.Context) {
	// Refresh at 80% of TTL to avoid serving stale keys.
	interval := time.Duration(float64(c.ttl) * 0.8)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.refresh() // best-effort; stale keys still serve
		}
	}
}

func discoverJWKSURL(client *http.Client, issuerURL string) (string, error) {
	url := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching discovery doc: %w", err)
	}
	defer resp.Body.Close()

	var doc discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decoding discovery doc: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", errors.New("discovery doc missing jwks_uri")
	}
	return doc.JWKSURI, nil
}

func parseJWK(k jwkKey) (any, error) {
	switch k.Kty {
	case "RSA":
		return parseRSAJWK(k)
	case "EC":
		return parseECJWK(k)
	default:
		return nil, fmt.Errorf("unsupported key type: %s", k.Kty)
	}
}

func parseRSAJWK(k jwkKey) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decoding n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decoding e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

func parseECJWK(k jwkKey) (*ecdsa.PublicKey, error) {
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("decoding x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("decoding y: %w", err)
	}

	curve, err := curveForName(k.Crv)
	if err != nil {
		return nil, err
	}

	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}

func curveForName(name string) (elliptic.Curve, error) {
	switch name {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("unsupported curve: %s", name)
	}
}
