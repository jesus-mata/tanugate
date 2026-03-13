package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/jesus-mata/tanugate/internal/config"
)

// APIKeyAuthenticator validates requests using a static API key sent in a
// configurable header.
type APIKeyAuthenticator struct {
	header string
	keys   []config.APIKeyEntry
}

// NewAPIKeyAuthenticator returns a ready-to-use API key authenticator.
func NewAPIKeyAuthenticator(cfg *config.APIKeyConfig) *APIKeyAuthenticator {
	header := cfg.Header
	if header == "" {
		header = "X-API-Key"
	}
	return &APIKeyAuthenticator{
		header: header,
		keys:   cfg.Keys,
	}
}

// Authenticate reads the configured header and compares the value against all
// known keys using constant-time comparison.
func (a *APIKeyAuthenticator) Authenticate(r *http.Request) (*AuthResult, error) {
	key := r.Header.Get(a.header)
	if key == "" {
		return nil, errors.New("missing API key")
	}

	for _, entry := range a.keys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(entry.Key)) == 1 {
			return &AuthResult{
				Subject: entry.Name,
				Name:    entry.Name,
				Claims:  map[string]any{"key_name": entry.Name},
			}, nil
		}
	}

	return nil, errors.New("invalid API key")
}
