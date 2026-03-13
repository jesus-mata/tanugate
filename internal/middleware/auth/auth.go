package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
)

// Authenticator validates an incoming request and returns an AuthResult on
// success or an error when authentication fails.
type Authenticator interface {
	Authenticate(r *http.Request) (*AuthResult, error)
}

// AuthResult carries identity information extracted during authentication.
type AuthResult struct {
	Subject  string         // User/client identifier
	Claims   map[string]any // All claims (JWT/OIDC) or metadata
	Name     string         // Human-readable name (API key)
	Provider string         // Name of the provider that authenticated the request
}

type authResultKey struct{}

// ResultFromContext retrieves the AuthResult stored in ctx by the auth
// middleware. It returns nil if no result is present.
func ResultFromContext(ctx context.Context) *AuthResult {
	ar, _ := ctx.Value(authResultKey{}).(*AuthResult)
	return ar
}

// WithAuthResult returns a new context carrying the given AuthResult.
func WithAuthResult(ctx context.Context, result *AuthResult) context.Context {
	return context.WithValue(ctx, authResultKey{}, result)
}

// NewAuthenticator creates an Authenticator for the given provider
// configuration. It returns an error if the provider type is unknown or the
// configuration is invalid.
func NewAuthenticator(provider config.AuthProvider) (Authenticator, error) {
	switch provider.Type {
	case "jwt":
		if provider.JWT == nil {
			return nil, fmt.Errorf("auth provider type %q requires jwt config", provider.Type)
		}
		return NewJWTAuthenticator(provider.JWT)
	case "apikey":
		if provider.APIKey == nil {
			return nil, fmt.Errorf("auth provider type %q requires api_key config", provider.Type)
		}
		return NewAPIKeyAuthenticator(provider.APIKey), nil
	case "oidc":
		if provider.OIDC == nil {
			return nil, fmt.Errorf("auth provider type %q requires oidc config", provider.Type)
		}
		return NewOIDCAuthenticator(provider.OIDC)
	default:
		return nil, fmt.Errorf("unknown auth provider type: %q", provider.Type)
	}
}

// Middleware returns a middleware.Middleware that authenticates requests using
// the provided authenticator map. The map keys correspond to the provider
// names defined in the gateway configuration. A provider name of "none"
// bypasses authentication entirely.
func Middleware(logger *slog.Logger, authenticators map[string]Authenticator) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mr := router.RouteFromContext(r.Context())
			if mr == nil || mr.Config.Auth == nil || len(mr.Config.Auth.Providers) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			providers := mr.Config.Auth.Providers
			if len(providers) == 1 && providers[0] == "none" {
				next.ServeHTTP(w, r)
				return
			}

			var authErrors []string
			for _, name := range providers {
				authn, ok := authenticators[name]
				if !ok {
					logger.Error("auth provider not found", "provider", name, "route", mr.Config.Name)
					writeError(w, http.StatusInternalServerError, "misconfigured auth provider")
					return
				}

				result, err := authn.Authenticate(r)
				if err != nil {
					authErrors = append(authErrors, fmt.Sprintf("%s: %s", name, err.Error()))
					continue
				}

				result.Provider = name
				ctx := WithAuthResult(r.Context(), result)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			logger.Warn("all auth providers failed",
				"route", mr.Config.Name,
				"providers", strings.Join(providers, ","),
				"errors", strings.Join(authErrors, "; "),
			)
			writeError(w, http.StatusUnauthorized, "authentication failed")
		})
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	errKey := "unauthorized"
	if status == http.StatusForbidden {
		errKey = "forbidden"
	} else if status == http.StatusInternalServerError {
		errKey = "internal_error"
	}
	json.NewEncoder(w).Encode(map[string]string{
		"error":   errKey,
		"message": message,
	})
}
