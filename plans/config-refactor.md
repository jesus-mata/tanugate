# Middleware Configuration Refactor Plan

## Context

The current config has middleware as typed fields on each route (`auth:`, `rate_limit:`, `cors:`, `transform:`) with a hardcoded execution order in `buildHandler()`. This prevents users from:
- Running the same middleware type multiple times (e.g., rate-limit before and after auth)
- Controlling middleware execution order per route
- Reusing middleware config across routes

The refactor introduces **Option D1**: named middleware definitions declared globally, referenced by routes with optional config overrides, and user-controlled ordering.

## Target Config Schema

```yaml
server: { ... }
logging: { ... }
cors: { ... }                        # global CORS for preflight handling (stays)
rate_limit: { backend: "redis", redis: { ... } }  # infrastructure (stays)
auth_providers: { ... }              # provider definitions (stays)

middleware_definitions:               # NEW: reusable typed definitions
  ip-limiter:
    type: rate_limit
    config:
      requests: 100
      window: 60s
      key_source: "ip"
  jwt-auth:
    type: auth
    config:
      providers: [jwt_default]
  my-transform:
    type: transform
    config:
      request: { headers: { add: { X-Gateway: "tanugate" } } }
      response: { headers: { add: { X-Served-By: "gateway" } } }

middlewares:                          # NEW: global middleware (prepended to all routes)
  - ref: ip-limiter

routes:
  - name: user-service
    match: { ... }
    upstream: { ... }
    retry: { ... }                   # stays on route (proxy behavior)
    circuit_breaker: { ... }         # stays on route (proxy behavior)
    skip_middlewares: [ip-limiter]   # NEW: exclude specific globals
    middlewares:                      # NEW: ordered middleware refs
      - ref: cors-override
      - ref: ip-limiter
        config: { requests: 200 }   # per-route override
      - ref: jwt-auth
```

## Design Decisions

| Decision | Choice |
|----------|--------|
| Config model | D1: definitions + refs with overrides |
| Retry/CircuitBreaker | Separate route fields (proxy behavior, not middleware) |
| Global + per-route | Globals prepend; `skip_middlewares: [name]` to exclude specific ones |
| Backward compat | Clean break |
| CORS | Middleware definition, validated to be first in list |
| Transform | Single type with request/response sub-config |
| Auth providers | Stay top-level; auth middleware references by name |
| Field naming | `key_source`, `requests` (breaking rename from `requests_per_window`) |

## Implementation Phases

### Phase 1: Config Structs + Resolution

**Files to modify:**
- `internal/config/config.go` — new structs, remove old route middleware fields, update validation

**New file:**
- `internal/config/resolve.go` — middleware ref resolution + yaml.Node merging

#### New structs

```go
type MiddlewareDefinition struct {
    Type   string    `yaml:"type"`
    Config yaml.Node `yaml:"config"`
}

type MiddlewareRef struct {
    Ref    string    `yaml:"ref"`
    Config yaml.Node `yaml:"config,omitempty"`  // optional per-route override
}

// Replaces RouteLimitConfig (field rename: requests_per_window -> requests)
type RateLimitConfig struct {
    Requests  int           `yaml:"requests" jsonschema:"minimum=1"`
    Window    time.Duration `yaml:"window"`
    KeySource string        `yaml:"key_source"`
    Algorithm string        `yaml:"algorithm" jsonschema:"default=sliding_window,enum=sliding_window,enum=leaky_bucket"`
}

type AuthMiddlewareConfig struct {
    Providers []string `yaml:"providers"`
}

type ResolvedMiddleware struct {
    Name   string  // definition name (for keys, metrics, logging)
    Type   string  // "cors" | "rate_limit" | "auth" | "transform"
    Config any     // typed: *CORSConfig, *RateLimitConfig, *AuthMiddlewareConfig, *TransformConfig
}
```

#### Updated GatewayConfig

```go
type GatewayConfig struct {
    Server                ServerConfig                       `yaml:"server"`
    Logging               LoggingConfig                      `yaml:"logging"`
    CORS                  CORSConfig                         `yaml:"cors"`
    RateLimit             RateLimitGlobalConfig               `yaml:"rate_limit"`
    AuthProviders         map[string]AuthProvider             `yaml:"auth_providers"`
    MiddlewareDefinitions map[string]MiddlewareDefinition     `yaml:"middleware_definitions"`
    Middlewares           []MiddlewareRef                     `yaml:"middlewares"`
    Routes                []RouteConfig                       `yaml:"routes"`
}
```

#### Updated RouteConfig

```go
type RouteConfig struct {
    Name            string                `yaml:"name" jsonschema:"required"`
    Match           MatchConfig           `yaml:"match" jsonschema:"required"`
    Upstream        UpstreamConfig        `yaml:"upstream" jsonschema:"required"`
    Retry           *RetryConfig          `yaml:"retry"`
    CircuitBreaker  *CircuitBreakerConfig `yaml:"circuit_breaker"`
    SkipMiddlewares []string              `yaml:"skip_middlewares"`
    Middlewares     []MiddlewareRef       `yaml:"middlewares"`
}
```

**Removed fields from RouteConfig**: `CORS`, `Auth`, `RateLimit`, `Transform`
**Removed structs**: `RouteLimitConfig`, `RouteAuthConfig`

#### resolve.go — Resolution logic

- `ResolveMiddlewares(refs []MiddlewareRef, defs map[string]MiddlewareDefinition) ([]ResolvedMiddleware, error)`
  - For each ref: lookup in definitions, merge override yaml.Node, decode into typed struct based on `Type`
- `mergeYAMLNodes(base, override yaml.Node) yaml.Node`
  - Shallow merge of top-level mapping keys (override wins)
- `decodeTypedConfig(typeName string, node yaml.Node) (any, error)`
  - Switch on type: `"cors"` -> `*CORSConfig`, `"rate_limit"` -> `*RateLimitConfig`, `"auth"` -> `*AuthMiddlewareConfig`, `"transform"` -> `*TransformConfig`

Valid middleware types: `cors`, `rate_limit`, `auth`, `transform`

#### Updated validation in semanticErrors()

- Unknown ref → error
- Unknown middleware type → error
- CORS not first in resolved chain (global+route) → error
- `key_source: "claim:*"` without preceding auth → error
- Auth providers reference valid `auth_providers` → error
- Rate limit: `requests > 0`, `window > 0`, valid algorithm, valid key_source format
- `skip_middlewares` entries must match names in global `Middlewares` refs

#### Updated applyDefaults()

- Remove old per-route defaults for `RateLimit.Algorithm`
- Defaults now applied during resolution in `decodeTypedConfig` (e.g., algorithm defaults to `sliding_window`)

---

### Phase 2: Schema Generation

**File to modify:**
- `internal/config/schema.go`

Add custom mapper for `yaml.Node` → permissive schema (accepts any valid YAML mapping). The per-type validation happens in `semanticErrors()` after resolution, not in the JSON Schema pass.

```go
if t == yamlNodeType {
    return &invopop.Schema{Description: "Type-specific middleware configuration"}
}
```

---

### Phase 3: Middleware Registry + Decoupled Constructors

**New file:**
- `internal/pipeline/registry.go` — factory registry

**Files to modify:**
- `internal/middleware/ratelimit/ratelimit.go` — add `NewRateLimitMiddleware()` with config bound at construction
- `internal/middleware/auth/auth.go` — add `NewAuthMiddleware()` with providers bound at construction

#### Registry design

```go
package pipeline

type FactoryDeps struct {
    Limiter        ratelimit.Limiter
    Authenticators map[string]auth.Authenticator
    Metrics        *observability.MetricsCollector
    TrustedProxies []*net.IPNet
    Logger         *slog.Logger
}

type Factory func(deps *FactoryDeps, routeName, mwName string, cfg any) (middleware.Middleware, error)

type Registry struct { factories map[string]Factory }
func NewRegistry() *Registry
func (r *Registry) Register(typeName string, f Factory)
func (r *Registry) Build(...) (middleware.Middleware, error)
func DefaultRegistry() *Registry  // registers all built-in types
```

#### New constructors (decouple from route context)

**ratelimit.go** — `NewRateLimitMiddleware(cfg *config.RateLimitConfig, routeName, mwName string, limiter Limiter, metrics, trustedProxies)`
- Config closed over at construction (no `router.RouteFromContext` lookup)
- Composite key: `rl:{algo}:{route}:{mwName}:{extractedKey}` (added `mwName` for uniqueness)
- Metrics: pass `mwName` for the new label

**auth.go** — `NewAuthMiddleware(logger, authenticators, providers []string)`
- Providers closed over (no `mr.Config.Auth.Providers` lookup)
- Keep existing auth result context storage

The old `RateLimit()` and `Middleware()` functions can be removed (clean break).

---

### Phase 4: Metrics Updates

**File to modify:**
- `internal/observability/metrics.go`

Add `"middleware"` label to `RateLimitRejected` and `RateLimitErrors`:
```go
[]string{"route", "middleware"}
```

All call sites updated in Phase 3's new constructors.

---

### Phase 5: Pipeline Builder

**New file:**
- `internal/pipeline/builder.go`

**File to modify:**
- `cmd/gateway/main.go` — delegate to pipeline builder

#### Builder algorithm

```go
func BuildHandler(cfg *config.GatewayConfig, deps *FactoryDeps, registry *Registry) (http.Handler, error) {
    // 1. Resolve global middlewares
    globalResolved := config.ResolveMiddlewares(cfg.Middlewares, cfg.MiddlewareDefinitions)

    // 2. Per route:
    for route := range cfg.Routes {
        // a. Inner handler: proxy
        handler := proxy.NewProxyHandler(route)

        // b. Wrap with circuit_breaker + retry (stays as route fields)
        handler = wrapResilience(route, handler, deps)

        // c. Resolve per-route middlewares
        routeResolved := config.ResolveMiddlewares(route.Middlewares, cfg.MiddlewareDefinitions)

        // d. Filter globals: remove skip_middlewares entries
        effectiveGlobals := filterSkipped(globalResolved, route.SkipMiddlewares)

        // e. Full chain: effectiveGlobals + routeResolved
        allMiddlewares := append(effectiveGlobals, routeResolved...)

        // f. Separate transform from chain middleware
        //    Transform wraps proxy (response side closest, request side outside)
        //    Other middleware wraps in declared order (outermost = first in list)

        // g. Apply transform around proxy+resilience handler
        // h. Apply remaining chain middleware in reverse (outermost first)

        handlers[route.Name] = handler
    }

    // 3. Router + global chain (recovery, requestID, logging, metrics, CORS)
    // Global chain unchanged
}
```

**Transform special handling**: Extract any `type: "transform"` entry from the resolved list. Apply it directly around the proxy+resilience handler (request transform wrapping response transform wrapping handler). The remaining middleware entries build the outer chain.

**main.go changes**: `buildHandler()` becomes a thin wrapper calling `pipeline.BuildHandler()`. `handleReload()` calls the same function.

---

### Phase 6: Config Reload + Diff

**File to modify:**
- `internal/config/config.go`

#### routeChanged() update

Remove comparisons for old fields (`routeAuthEqual`, `routeLimitEqual`, `corsChanged`, `transformChanged`).
Add: `middlewareRefsEqual(a.Middlewares, b.Middlewares)`, `stringSliceEqual(a.SkipMiddlewares, b.SkipMiddlewares)`.

#### DiffSummary() update

Add detection for:
- `middleware_definitions` changes (added/removed/modified)
- Global `middlewares` list changes
- Keep existing route add/remove/reorder detection

#### NonReloadableChanges()

No change needed — `middleware_definitions` and `middlewares` are reloadable (handler is fully rebuilt). Infrastructure config (`rate_limit.backend`, `auth_providers`) remains non-reloadable.

#### Cleanup

Remove now-unused comparison functions: `routeAuthEqual`, `routeLimitEqual`, `transformChanged`, `directionTransformChanged`, `headerTransformChanged`, `bodyTransformChanged`.

---

### Phase 7: Example Configs

**Files to rewrite:**
- `config/gateway.example.yaml` — full reference with all middleware types, global middlewares, skip examples, overrides
- `config/gateway.yaml` — active config converted to new format

---

### Phase 8: Tests

**Files to modify:**
- `internal/config/config_test.go` — update all existing config tests for new format

**New files:**
- `internal/config/resolve_test.go` — resolution, merging, type decoding
- `internal/pipeline/registry_test.go` — factory registration and build
- `internal/pipeline/builder_test.go` — pipeline construction

**File to modify:**
- `internal/integration_test.go` — update `newTestGateway()` and `buildTestHandler()` for new config format. All 26+ existing tests should pass after config format migration.

#### Key test cases

**Config validation:**
- Unknown ref → error
- Unknown type → error
- CORS not first → error
- `claim:` key_source without preceding auth → error
- `skip_middlewares` unknown name → error
- Valid new-format config → success

**Resolution:**
- Basic ref resolution
- Override merging (shallow merge, override wins)
- Type-specific decoding

**Pipeline:**
- Middleware executes in declared order
- Global middlewares prepend
- `skip_middlewares` excludes correctly
- Transform wraps proxy correctly
- Retry/CB stay as proxy wrappers

---

### Phase 9: Cleanup

- Remove old `RouteLimitConfig` struct (replaced by `RateLimitConfig`)
- Remove old `RouteAuthConfig` struct (replaced by `AuthMiddlewareConfig`)
- Remove old `ratelimit.RateLimit()` function
- Remove old `auth.Middleware()` function
- Remove unused diff helpers (`routeAuthEqual`, `routeLimitEqual`, etc.)
- Clean up `cmd/gateway/main.go` imports

---

## File Summary

| File | Action | Phase |
|------|--------|-------|
| `internal/config/config.go` | Modify: new structs, update validation, update defaults, update diff | 1, 6 |
| `internal/config/resolve.go` | **New**: resolution, merging, type decoding | 1 |
| `internal/config/schema.go` | Modify: yaml.Node mapper | 2 |
| `internal/pipeline/registry.go` | **New**: factory registry | 3 |
| `internal/pipeline/builder.go` | **New**: pipeline construction | 5 |
| `internal/middleware/ratelimit/ratelimit.go` | Modify: add `NewRateLimitMiddleware`, update composite key | 3 |
| `internal/middleware/auth/auth.go` | Modify: add `NewAuthMiddleware` | 3 |
| `internal/observability/metrics.go` | Modify: add `middleware` label | 4 |
| `cmd/gateway/main.go` | Modify: delegate to pipeline builder | 5 |
| `config/gateway.example.yaml` | Rewrite | 7 |
| `config/gateway.yaml` | Rewrite | 7 |
| `internal/config/resolve_test.go` | **New** | 8 |
| `internal/pipeline/registry_test.go` | **New** | 8 |
| `internal/pipeline/builder_test.go` | **New** | 8 |
| `internal/config/config_test.go` | Modify | 8 |
| `internal/integration_test.go` | Modify | 8 |

## Risks

1. **Transform positioning** — response transform must wrap proxy; request transform wraps outside. The builder must extract transform entries and apply them specially rather than treating them as regular chain middleware.

2. **yaml.Node merge** — deep merging is complex. Start with shallow (top-level key override) which covers the primary use case (override `requests: 200`).

3. **Rate limit key change** — composite key format changes from `rl:{algo}:{route}:{key}` to `rl:{algo}:{route}:{mwName}:{key}`. Existing Redis keys become orphaned. Acceptable for clean break; document in release notes.

4. **JSON Schema strictness** — `yaml.Node` fields produce permissive schema for `config:` sections. Per-type validation shifts to `semanticErrors()`. Schema still catches unknown top-level fields.

5. **Integration test volume** — 26+ tests reference old config format via `newTestGateway()`. All must be migrated. The `TestBuildTestHandler_MiddlewareOrderMatchesProduction` test needs rewriting since `buildTestHandler` no longer mirrors `buildHandler` line-for-line (both use `pipeline.BuildHandler`).

## Verification

1. `go build ./...` — compiles
2. `go test ./...` — all tests pass
3. `go run ./cmd/gateway validate -config config/gateway.example.yaml` — validates new format
4. `go run ./cmd/gateway schema` — generates valid JSON Schema for new format
5. Start gateway with new config, send requests through routes, verify middleware executes in declared order
6. Test SIGHUP reload with modified middleware definitions
7. Verify rate limit keys include middleware name (check Redis or memory limiter)
8. Verify Prometheus metrics include `middleware` label on rate limit counters
