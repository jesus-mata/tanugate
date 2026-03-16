# Plan: Fix All P0 + P1 Code Review Issues

## Context

A 5-agent `api-gateway-reviewer` team reviewed all unstaged code changes in the tanugate API gateway. The review found 1 P0 and 7 P1 issues affecting correctness, safety, and production readiness. This plan addresses all 8 issues. GH issue #39 tracks the P0 item.

---

## Implementation Order

Ordered from trivial → complex, with independent fixes first.

---

### Fix 1: Nil cfg guard in NewRateLimitMiddleware
**Files:**
- `internal/middleware/ratelimit/ratelimit.go` — add nil check at top of function
- `internal/pipeline/registry.go` — add nil check after type assertion in `rateLimitFactory`
- `internal/middleware/ratelimit/ratelimit_test.go` — add `TestNewRateLimitMiddleware_NilConfig`

**Change:** If `cfg == nil`, return a pass-through middleware (calls next directly). In factory, return error if nil.

---

### Fix 2: Empty providers guard in NewAuthMiddleware
**Files:**
- `internal/middleware/auth/auth.go` — add `len(providers) == 0` check before the `"none"` check
- `internal/middleware/auth/auth_test.go` — add `TestNewAuthMiddleware_EmptyProviders`

**Change:** Empty providers → return 500 with `"misconfigured auth middleware"`. This is a programmer error, not an auth failure.

---

### Fix 3: Downgrade proxy Director log to Debug
**Files:**
- `internal/proxy/proxy.go` line 58

**Change:** `slog.Info(` → `slog.Debug(`. One-liner. Access logging already handled by Logging middleware.

---

### Fix 4: Implement `claim:` key_source (P0, GH #39)
**Files:**
- `internal/middleware/ratelimit/ratelimit.go` — add `claim:` case in `extractKey()`
- `internal/middleware/ratelimit/ratelimit_test.go` — add claim extraction tests

**Change:** Add between `header:` case and `default`:
```go
case strings.HasPrefix(keySource, "claim:"):
    claimName := strings.TrimPrefix(keySource, "claim:")
    ar := auth.ResultFromContext(r.Context())
    if ar == nil || ar.Claims == nil {
        slog.Warn("rate limit claim key: no auth result in context, falling back to IP",
            "claim", claimName)
        return extractIP(r, trustedProxies)
    }
    val, ok := ar.Claims[claimName]
    if !ok {
        slog.Warn("rate limit claim key not found, falling back to IP",
            "claim", claimName)
        return extractIP(r, trustedProxies)
    }
    return fmt.Sprintf("%v", val)
```

**New import:** `"github.com/jesus-mata/tanugate/internal/middleware/auth"` (no circular dependency — verified).

**Tests:**
- `TestExtractKey_Claim_Present` — inject `auth.WithAuthResult`, verify claim value returned
- `TestExtractKey_Claim_NoAuthResult` — no auth in context, verify IP fallback
- `TestExtractKey_Claim_MissingClaim` — auth present but claim absent, verify IP fallback
- `TestExtractKey_Claim_NonStringValue` — numeric claim, verify `fmt.Sprintf("%v", val)` output

---

### Fix 5: Metrics `route=unknown` via mutable RouteHolder
**Files:**
- `internal/router/router.go` — add `RouteHolder` type and context helpers
- `internal/observability/metrics.go` — inject holder before dispatch, read after
- `internal/pipeline/pipeline_test.go` — verify metrics have correct route label

**router.go additions:**
```go
type routeHolderKey struct{}

type RouteHolder struct {
    Route *MatchedRoute
}

func WithRouteHolder(ctx context.Context) (context.Context, *RouteHolder) {
    h := &RouteHolder{}
    return context.WithValue(ctx, routeHolderKey{}, h), h
}

func RouteHolderFromContext(ctx context.Context) *RouteHolder {
    h, _ := ctx.Value(routeHolderKey{}).(*RouteHolder)
    return h
}
```

**router.go ServeHTTP** — after building `mr` (line ~102), before `ctx := context.WithValue(...)`:
```go
if holder := RouteHolderFromContext(r.Context()); holder != nil {
    holder.Route = mr
}
```

**metrics.go Middleware()** — replace route reading:
```go
ctx, holder := router.WithRouteHolder(r.Context())
next.ServeHTTP(ww, r.WithContext(ctx))
route := "unknown"
if holder.Route != nil {
    route = holder.Route.Config.Name
}
```

---

### Fix 6: Shared transport with cleanup on reload
**Files:**
- `internal/proxy/proxy.go` — accept `*http.Transport` parameter
- `internal/pipeline/builder.go` — create shared transport, return cleanup function
- `cmd/gateway/main.go` — store cleanup in handlerRef, call on reload/shutdown

**proxy.go:** Change `NewProxyHandler` signature to accept transport:
```go
func NewProxyHandler(routeCfg *config.RouteConfig, transport http.RoundTripper) http.Handler
```
Use provided transport in `httputil.ReverseProxy`. If nil, create default (backward compat).

**builder.go:** Change `BuildHandler` return to `(http.Handler, func(), error)`. Create one shared transport, pass to all routes. Return cleanup that calls `transport.CloseIdleConnections()`. Per-route timeout handled via `context.WithTimeout` in proxy Director using `route.Upstream.Timeout`.

**main.go:** Update `handlerRef` struct to include `cleanup func()`. In `handleReload`: call `old.cleanup()` after swapping. In shutdown: call `current.cleanup()`.

---

### Fix 7: Add MaxHeaderBytes to ServerConfig
**Files:**
- `internal/config/config.go` — add field to `ServerConfig`, default in `applyDefaults`, add to `NonReloadableChanges`
- `cmd/gateway/main.go` — set `MaxHeaderBytes` on `http.Server`
- `config/gateway.example.yaml` — document the field
- `internal/config/config_test.go` — extend defaults and reload tests

**Default:** 1 MB (`1 << 20`), same as Go's `http.DefaultMaxHeaderBytes`. Non-breaking.

---

### Fix 8: Hard error for unresolved env vars in sensitive fields
**Files:**
- `internal/config/config.go` — add `hasUnresolvedEnvVar()` helper, add checks in `semanticErrors()`
- `internal/config/config_test.go` — add tests for each sensitive field

**Sensitive fields to check:**
- `AuthProviders[*].JWT.Secret`
- `AuthProviders[*].APIKey.Keys[*].Key`
- `AuthProviders[*].OIDC.ClientSecret`
- `RateLimit.Redis.Password`

**Change:** In `semanticErrors()`, iterate auth providers and redis config. If `hasUnresolvedEnvVar(field)` is true, append error. Config load fails with clear message like: `auth_providers.main.jwt.secret contains unresolved env var "${JWT_SECRET}" — set the variable or use a literal value`.

---

## Verification

After all fixes:

1. `go test ./... -race` — all packages pass, no races
2. `go vet ./...` — clean
3. `golangci-lint run` — 0 issues
4. Manual verification:
   - Start gateway with a config using `claim:sub` key_source → rate limiting uses JWT sub claim
   - Trigger SIGHUP reload → old transport connections cleaned up, no FD leak
   - Check `/metrics` endpoint → `gateway_requests_total` shows actual route names, not "unknown"
   - Start with unset `${JWT_SECRET}` → config load fails with clear error
   - Check logs at Info level → no per-request proxy Director log spam
