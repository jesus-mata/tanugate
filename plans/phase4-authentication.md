# Phase 4: Authentication — Implementation Plan

## Overview

Phase 4 implements the authentication layer with three pluggable strategies (JWT, API Key, OIDC) unified behind a common `Authenticator` interface. An auth middleware reads the route's configured provider from context and either enriches the context with an `AuthResult` or returns 401/403.

**Depends on:** Phase 1 (middleware chain, config structs, router context)
**Can run in parallel with:** Phases 3, 5, 6, 7
**External dependency:** `github.com/golang-jwt/jwt/v5` v5.2+

---

## 1. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `internal/middleware/auth/auth.go` | Create | Authenticator interface, AuthResult, factory, middleware, context helpers |
| `internal/middleware/auth/auth_test.go` | Create | Factory dispatch, middleware skip/propagation tests |
| `internal/middleware/auth/jwt.go` | Create | JWT validation (HMAC + RSA + ECDSA) |
| `internal/middleware/auth/jwt_test.go` | Create | Valid/expired/wrong-sig/issuer/audience tests |
| `internal/middleware/auth/apikey.go` | Create | API key validation with constant-time compare |
| `internal/middleware/auth/apikey_test.go` | Create | Valid/invalid key, timing attack resistance |
| `internal/middleware/auth/oidc.go` | Create | OIDC JWKS fetching/caching + introspection |
| `internal/middleware/auth/oidc_test.go` | Create | Mock JWKS endpoint, token validation, introspection |
| `cmd/gateway/main.go` | Modify | Build auth providers, wire into per-route chains |

---

## 2. Core Types

### Authenticator Interface

```go
type Authenticator interface {
    Authenticate(r *http.Request) (*AuthResult, error)
}

type AuthResult struct {
    Subject string         // User/client identifier
    Claims  map[string]any // All claims (JWT/OIDC) or metadata
    Name    string         // Human-readable name (API key)
}
```

### Factory

```go
func NewAuthenticator(provider config.AuthProvider) (Authenticator, error)
```

Dispatches on `provider.Type`: `"jwt"` → `JWTAuthenticator`, `"apikey"` → `APIKeyAuthenticator`, `"oidc"` → `OIDCAuthenticator`.

### Auth Middleware

```go
func Middleware(authenticators map[string]Authenticator) middleware.Middleware
```

- Reads `MatchedRoute` from context → gets `auth.provider` name
- If provider is `"none"`, skip authentication
- Calls `authenticator.Authenticate(r)`
- On success: stores `AuthResult` in context, calls next
- On error: returns 401 JSON

### Context Helpers

```go
func ResultFromContext(ctx context.Context) *AuthResult
func WithAuthResult(ctx context.Context, result *AuthResult) context.Context
```

---

## 3. JWT Authenticator (`jwt.go`)

### Token Extraction
- Reads `Authorization: Bearer <token>` header
- Parses with `golang-jwt/jwt/v5`

### Key Resolution
- HMAC (HS256/HS384/HS512): `[]byte(config.Secret)` as signing key
- RSA (RS256/RS384/RS512): load PEM from `config.PublicKeyFile` via `x509.ParsePKCS1PublicKey` or `x509.ParsePKIXPublicKey`
- ECDSA (ES256/ES384/ES512): load PEM via `x509.ParseECPrivateKey` or `x509.ParsePKIXPublicKey`

### Claims Validation
- `exp` — expiry (built into jwt/v5)
- `iss` — issuer (validated if config is non-empty)
- `aud` — audience (validated if config is non-empty)

### Key File Caching
Load and parse the PEM file once at construction time, not per request.

---

## 4. API Key Authenticator (`apikey.go`)

### Validation
- Reads configured header (e.g., `X-API-Key`)
- Iterates through all keys, comparing with `crypto/subtle.ConstantTimeCompare`
- On match: returns `AuthResult` with key's `name`
- No match: returns error → 401

### Security Notes
- Constant-time comparison prevents timing attacks
- Must compare all keys even after a match to maintain constant time (or compare until first match with constant-time per comparison — acceptable since the list size is known)

---

## 5. OIDC Authenticator (`oidc.go`)

### Initialization
1. If `jwks_url` is set, use directly
2. Else, fetch `{issuer_url}/.well-known/openid-configuration`, extract `jwks_uri`
3. Fetch JWKS and cache in memory
4. Start background refresh based on `cache_ttl`

### Token Validation
- Parse JWT header to get `kid`
- Look up signing key in cached JWKS by `kid`
- Validate signature, `exp`, `aud`

### Introspection Mode (if `introspection_url` set)
- POST token to introspection endpoint (RFC 7662)
- Auth with `client_id` + `client_secret` (HTTP Basic)
- Check `active: true` in response

### JWKS Cache
```go
type jwksCache struct {
    mu       sync.RWMutex
    keys     map[string]crypto.PublicKey // kid → key
    expiry   time.Time
    ttl      time.Duration
    fetchURL string
}
```

Background goroutine refreshes keys before expiry. Accepts `context.Context` for shutdown.

---

## 6. Error Responses

| Condition | Status | Body |
|---|---|---|
| Missing credentials | 401 | `{"error": "unauthorized", "message": "missing authorization header"}` |
| Malformed token | 401 | `{"error": "unauthorized", "message": "malformed token"}` |
| Invalid signature | 401 | `{"error": "unauthorized", "message": "invalid token signature"}` |
| Expired token | 401 | `{"error": "unauthorized", "message": "token expired"}` |
| Invalid API key | 401 | `{"error": "unauthorized", "message": "invalid API key"}` |
| Insufficient permissions | 403 | `{"error": "forbidden", "message": "insufficient permissions"}` |

---

## 7. Test Plan

### `auth_test.go`

| Test | Description |
|---|---|
| `TestNewAuthenticator_JWT` | Factory creates JWTAuthenticator |
| `TestNewAuthenticator_APIKey` | Factory creates APIKeyAuthenticator |
| `TestNewAuthenticator_OIDC` | Factory creates OIDCAuthenticator |
| `TestNewAuthenticator_Unknown` | Unknown type returns error |
| `TestMiddleware_SkipsNone` | Provider "none" → next called, no auth |
| `TestMiddleware_StoresAuthResult` | Successful auth → result in context |
| `TestMiddleware_Returns401` | Failed auth → 401 JSON |

### `jwt_test.go`

| Test | Description |
|---|---|
| `TestJWT_ValidHS256` | Valid token passes |
| `TestJWT_ExpiredToken` | Expired `exp` → 401 |
| `TestJWT_WrongSignature` | Bad signature → 401 |
| `TestJWT_IssuerMismatch` | Wrong `iss` → 401 |
| `TestJWT_AudienceMismatch` | Wrong `aud` → 401 |
| `TestJWT_MissingBearerToken` | No Authorization header → 401 |
| `TestJWT_MalformedHeader` | `Authorization: bad` → 401 |
| `TestJWT_ValidRS256` | RSA token validated with public key |
| `TestJWT_ClaimsInResult` | All claims available in AuthResult |

### `apikey_test.go`

| Test | Description |
|---|---|
| `TestAPIKey_ValidKey` | Correct key → success, name in result |
| `TestAPIKey_InvalidKey` | Wrong key → 401 |
| `TestAPIKey_MissingHeader` | No header → 401 |
| `TestAPIKey_ConstantTimeCompare` | Verify `subtle.ConstantTimeCompare` usage |
| `TestAPIKey_MultipleKeys` | Second key in list matches |

### `oidc_test.go`

| Test | Description |
|---|---|
| `TestOIDC_JWKSFetch` | Mock JWKS endpoint, keys cached |
| `TestOIDC_ValidToken` | Token with correct kid passes |
| `TestOIDC_UnknownKid` | Unknown kid → 401 |
| `TestOIDC_JWKSRefresh` | Keys refreshed after TTL |
| `TestOIDC_Introspection` | Mock introspection endpoint, active=true passes |
| `TestOIDC_IntrospectionInactive` | active=false → 401 |
| `TestOIDC_Discovery` | Well-known endpoint parsed for jwks_uri |

---

## 8. Implementation Sequence

```
Step 1: auth/auth.go (interface, factory, middleware, context helpers)
Step 2: auth/jwt.go (depends on golang-jwt/jwt/v5)
Step 3: auth/apikey.go (stdlib only)
Step 4: auth/oidc.go (stdlib + auth.go types)
Step 5: Tests for each (parallel)
Step 6: Wire into main.go
```

Steps 2, 3, 4 are independent.

---

## 9. Acceptance Criteria

- [ ] JWT validation covers HMAC, RSA, and ECDSA algorithms
- [ ] API key comparison uses `crypto/subtle.ConstantTimeCompare`
- [ ] OIDC JWKS are cached and refreshed based on `cache_ttl`
- [ ] Provider "none" bypasses auth entirely
- [ ] AuthResult stored in context for downstream middleware (rate limit `claim:` keying)
- [ ] All error responses follow `{"error":"...", "message":"..."}` format
- [ ] No secrets (tokens, keys) logged at any level
- [ ] All tests pass with `go test -race`
