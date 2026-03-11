package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCAuthenticator validates tokens using OIDC JWKS or token introspection.
type OIDCAuthenticator struct {
	verifier         *oidc.IDTokenVerifier // nil in introspection-only mode
	issuerURL        string
	audience         string
	introspectionURL string
	clientID         string
	clientSecret     string
	httpClient       *http.Client
	cancel           context.CancelFunc
}

type introspectionResponse struct {
	Active   bool   `json:"active"`
	Sub      string `json:"sub"`
	ClientID string `json:"client_id"`
}

// NewOIDCAuthenticator creates an OIDC authenticator. It supports three modes:
//   - Full discovery: issuer_url is set → uses OIDC discovery for JWKS
//   - Direct JWKS: jwks_url is set (no issuer_url) → fetches keys directly
//   - Introspection-only: introspection_url is set (no JWKS/issuer)
func NewOIDCAuthenticator(cfg *config.OIDCConfig) (*OIDCAuthenticator, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())

	a := &OIDCAuthenticator{
		issuerURL:        cfg.IssuerURL,
		audience:         cfg.Audience,
		introspectionURL: cfg.IntrospectionURL,
		clientID:         cfg.ClientID,
		clientSecret:     cfg.ClientSecret,
		httpClient:       httpClient,
		cancel:           cancel,
	}

	// Mode 3: Introspection-only — no verifier needed.
	if cfg.IntrospectionURL != "" && cfg.JWKSURL == "" && cfg.IssuerURL == "" {
		return a, nil
	}

	oidcCfg := buildOIDCConfig(cfg)

	// Inject our HTTP client into the context for go-oidc's HTTP calls.
	oidcCtx := oidc.ClientContext(ctx, httpClient)

	switch {
	case cfg.IssuerURL != "":
		// Mode 1: Full discovery.
		provider, err := oidc.NewProvider(oidcCtx, cfg.IssuerURL)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("oidc discovery: %w", err)
		}
		a.verifier = provider.Verifier(&oidcCfg)

	case cfg.JWKSURL != "":
		// Mode 2: Direct JWKS — no issuer known, force skip issuer check.
		oidcCfg.SkipIssuerCheck = true
		keySet := oidc.NewRemoteKeySet(oidcCtx, cfg.JWKSURL)
		a.verifier = oidc.NewVerifier(cfg.IssuerURL, keySet, &oidcCfg)

	default:
		cancel()
		return nil, errors.New("oidc: either jwks_url, issuer_url, or introspection_url is required")
	}

	return a, nil
}

// buildOIDCConfig translates our gateway config into go-oidc's verification config.
func buildOIDCConfig(cfg *config.OIDCConfig) oidc.Config {
	c := oidc.Config{
		SkipIssuerCheck: cfg.SkipIssuerCheck,
	}
	// go-oidc uses ClientID for audience checking.
	if cfg.Audience != "" {
		c.ClientID = cfg.Audience
	} else {
		c.SkipClientIDCheck = true
	}
	if len(cfg.AllowedAlgorithms) > 0 {
		c.SupportedSigningAlgs = cfg.AllowedAlgorithms
	}
	// Force skip issuer check when no issuer URL is configured.
	if cfg.IssuerURL == "" {
		c.SkipIssuerCheck = true
	}
	return c
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

	return a.verifyToken(r.Context(), tokenString)
}

// Stop cancels the background context used by the OIDC provider/key set.
func (a *OIDCAuthenticator) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *OIDCAuthenticator) verifyToken(ctx context.Context, rawToken string) (*AuthResult, error) {
	idToken, err := a.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, classifyVerifyError(err)
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("extracting claims: %w", err)
	}

	return &AuthResult{
		Subject: idToken.Subject,
		Claims:  claims,
	}, nil
}

func (a *OIDCAuthenticator) introspect(tokenString string) (*AuthResult, error) {
	data := url.Values{"token": {tokenString}}
	req, err := http.NewRequest(http.MethodPost, a.introspectionURL, strings.NewReader(data.Encode()))
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

// classifyVerifyError translates go-oidc verification errors into user-friendly messages.
func classifyVerifyError(err error) error {
	var expiredErr *oidc.TokenExpiredError
	if errors.As(err, &expiredErr) {
		return errors.New("token expired")
	}

	errMsg := err.Error()
	switch {
	case strings.Contains(errMsg, "issuer") || strings.Contains(errMsg, "different provider"):
		return errors.New("invalid token issuer")
	case strings.Contains(errMsg, "audience"):
		return errors.New("invalid token audience")
	default:
		return fmt.Errorf("invalid token: %w", err)
	}
}
