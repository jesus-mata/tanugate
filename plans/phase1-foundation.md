# Phase 1: Foundation (Skeleton + Config + Routing + Proxy) — Implementation Plan

## Overview

This phase establishes the project from scratch: Go module initialization, YAML config loading with environment variable substitution, regex-based routing, `httputil.ReverseProxy`-based forwarding with path rewriting, the middleware chain primitive, two foundational middleware (panic recovery and request ID), and the `main.go` entry point with graceful shutdown. At the end of this phase, the gateway loads a YAML file, matches incoming requests to routes via ordered regex evaluation, and proxies them to upstream services with path rewriting.

## Prerequisites

- Go 1.25 installed
- No prior code exists in the repository
- Single external dependency: `gopkg.in/yaml.v3`

---

## Step 0: Project Initialization

### 0.1 Create the directory structure

```
api-gateway/
  cmd/gateway/
  internal/config/
  internal/router/
  internal/proxy/
  internal/middleware/
  config/
```

### 0.2 Initialize the Go module

```bash
go mod init github.com/jesus-mata/tanugate
```

### 0.3 Add the sole external dependency

```bash
go get gopkg.in/yaml.v3
```

### 0.4 Create a minimal test config file

Create `config/gateway.yaml` with a minimal but complete configuration for development/testing purposes.

---

## Step 1: `internal/config/config.go` — Config Structs and Loader

**Package:** `config`

This is the single most important file in Phase 1 because every other component depends on the config structs. Even though many config sections (auth, rate limiting, circuit breaker, etc.) are not used until later phases, **all structs must be defined now** so the YAML can be fully parsed from the start.

### 1.1 Imports

```go
import (
    "fmt"
    "log/slog"
    "os"
    "regexp"
    "time"

    "gopkg.in/yaml.v3"
)
```

### 1.2 Define all config structs

Define **every** config struct from the spec in this single file. The full list (22 structs):

1. `GatewayConfig` — Top-level root struct
2. `ServerConfig` — HTTP server settings with `time.Duration` fields
3. `LoggingConfig` — Just `Level string`
4. `CORSConfig` — Origins, methods, headers, credentials, max age
5. `RateLimitGlobalConfig` — Backend selector + Redis config
6. `RedisConfig` — Addr, Password, DB
7. `AuthProvider` — Type + pointer sub-configs (JWT, APIKey, OIDC)
8. `JWTConfig` — Secret, PublicKeyFile, Algorithm, Issuer, Audience
9. `APIKeyConfig` — Header + Keys list
10. `APIKeyEntry` — Key + Name
11. `OIDCConfig` — IssuerURL, JWKSURL, IntrospectionURL, ClientID, ClientSecret, Audience, CacheTTL
12. `RouteConfig` — Name, Match, Upstream, Auth, optional middleware configs
13. `MatchConfig` — PathRegex, Methods
14. `UpstreamConfig` — URL, PathRewrite, Timeout
15. `RouteAuthConfig` — Provider name
16. `RouteLimitConfig` — RequestsPerWindow, Window, KeySource
17. `RetryConfig` — MaxRetries, InitialDelay, Multiplier, RetryableStatusCodes
18. `CircuitBreakerConfig` — FailureThreshold, SuccessThreshold, Timeout
19. `TransformConfig` — Request + Response DirectionTransform
20. `DirectionTransform` — Headers + Body
21. `HeaderTransform` — Add, Remove, Rename
22. `BodyTransform` — InjectFields, StripFields, RenameKeys

### 1.3 Implement `LoadConfig(path string) (*GatewayConfig, error)`

Four steps in sequence:

**Step A: Read the file** — `os.ReadFile(path)`

**Step B: Environment variable substitution**
```go
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
```
Replace all `${VAR}` occurrences in raw YAML text **before** unmarshalling. Log a warning for unset variables.

**Step C: Unmarshal YAML** — `yaml.Unmarshal([]byte(resolved), &cfg)`

**Step D: Apply defaults** — Private `applyDefaults(cfg *GatewayConfig)`:

| Field | Default |
|---|---|
| `Server.Host` | `"0.0.0.0"` |
| `Server.Port` | `8080` |
| `Server.ReadTimeout` | `30s` |
| `Server.WriteTimeout` | `30s` |
| `Server.IdleTimeout` | `120s` |
| `Server.ShutdownTimeout` | `15s` |
| `Logging.Level` | `"info"` |
| `RateLimit.Backend` | `"memory"` |
| Each route's `Upstream.Timeout` | `30s` (if zero) |

### 1.4 Test file: `internal/config/config_test.go`

| Test | Description |
|---|---|
| `TestLoadConfig_ValidFullConfig` | All sections populated, verify all fields parsed |
| `TestLoadConfig_DefaultsApplied` | Minimal YAML, verify defaults |
| `TestLoadConfig_EnvVarSubstitution` | `${TEST_SECRET}` resolves correctly |
| `TestLoadConfig_EnvVarNotSet` | Unset var resolves to empty string |
| `TestLoadConfig_InvalidYAML` | Malformed YAML returns error |
| `TestLoadConfig_FileNotFound` | Non-existent path returns error |
| `TestLoadConfig_DurationFields` | `"30s"`, `"1m"`, `"100ms"` parse correctly |
| `TestLoadConfig_MultipleEnvVarsInOneLine` | `"http://${HOST}:${PORT}"` resolves |

Use `t.Setenv()` for automatic cleanup and parallel-safety.

---

## Step 2: `internal/middleware/chain.go` — Middleware Type and Chain Composer

```go
type Middleware func(http.Handler) http.Handler

func Chain(middlewares ...Middleware) Middleware {
    return func(final http.Handler) http.Handler {
        for i := len(middlewares) - 1; i >= 0; i-- {
            final = middlewares[i](final)
        }
        return final
    }
}
```

### Tests: `internal/middleware/chain_test.go`

| Test | Description |
|---|---|
| `TestChain_ExecutionOrder` | Verify A → B → C → handler → C → B → A order |
| `TestChain_Empty` | Zero arguments, handler called directly |
| `TestChain_Single` | Single middleware wraps correctly |

---

## Step 3: `internal/middleware/recovery.go` — Panic Recovery

Catches panics via `defer recover()`, logs stack trace with `runtime.Stack`, returns 500 JSON:
```json
{"error": "internal_error", "message": "Internal server error"}
```

### Tests: `internal/middleware/recovery_test.go`

| Test | Description |
|---|---|
| `TestRecovery_PanicString` | `panic("test")` → 500 JSON |
| `TestRecovery_PanicError` | `panic(errors.New(...))` → 500 JSON |
| `TestRecovery_NoPanic` | Normal handler passes through |
| `TestRecovery_ResponseFormat` | Verify Content-Type and JSON body |

---

## Step 4: `internal/middleware/requestid.go` — Request ID Generation

### UUID v4 generation using `crypto/rand`:
```go
func generateUUID() string {
    var uuid [16]byte
    io.ReadFull(rand.Reader, uuid[:])
    uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
    uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant 10
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
```

### Middleware behavior:
- Reuses existing `X-Request-ID` if present (distributed tracing)
- Generates new UUID if absent
- Sets response header and stores in context

### Context accessors:
- `RequestIDFromContext(ctx) string`
- Unexported `requestIDKey struct{}`

### Tests: `internal/middleware/requestid_test.go`

| Test | Description |
|---|---|
| `TestRequestID_Generated` | New UUID in response, matches v4 format |
| `TestRequestID_Propagated` | Existing ID reused |
| `TestRequestID_InContext` | Context accessor returns correct value |
| `TestRequestID_Uniqueness` | 1000 IDs, no duplicates |
| `TestRequestIDFromContext_Empty` | Bare context returns `""` |

---

## Step 5: `internal/router/router.go` — Regex Route Matcher

### Types
- `MatchedRoute` — `Config *RouteConfig`, `PathParams map[string]string`
- `compiledRoute` — `name`, `regex *regexp.Regexp`, `methods map[string]bool`, `handler http.Handler`, `config *RouteConfig`
- `Router` — `routes []compiledRoute`

### Constructor: `New(configs []RouteConfig, handlers map[string]http.Handler) *Router`
- Compiles regex with `regexp.MustCompile` (panics on bad regex — fail fast)
- Builds methods map (nil = all methods allowed)

### ServeHTTP
- Evaluates routes in order, first match wins
- Extracts named capture groups via `SubexpNames()`
- Stores `MatchedRoute` in context
- No match → 404 JSON

### Context accessor: `RouteFromContext(ctx) *MatchedRoute`
### Helper: `WithMatchedRoute(ctx, mr) context.Context` — for test injection

### Tests: `internal/router/router_test.go`

| Test | Description |
|---|---|
| `TestRouter_BasicMatch` | Single route matches |
| `TestRouter_NamedCaptureGroups` | `(?P<id>[^/]+)` extracts correctly |
| `TestRouter_MultipleCaptureGroups` | Two groups both extracted |
| `TestRouter_FirstMatchWins` | Specific route before general route |
| `TestRouter_MethodFiltering` | Wrong method falls through to 404 |
| `TestRouter_AllMethodsAllowed` | Nil methods, any method matches |
| `TestRouter_NoMatch_404` | 404 JSON response |
| `TestRouter_ContextContainsMatchedRoute` | `RouteFromContext` returns correct data |
| `TestRouteFromContext_NilWhenMissing` | Bare context returns nil |

---

## Step 6: `internal/proxy/proxy.go` — Reverse Proxy

### `NewProxyHandler(routeCfg *RouteConfig) http.Handler`

Creates `httputil.ReverseProxy` **once per route** at startup for connection pooling.

**Director:**
- Rewrites `URL.Scheme`, `URL.Host`, `req.Host`
- Applies path rewrite using named capture groups from context
- If no `path_rewrite`, forwards original path

**Transport:**
```go
&http.Transport{
    ResponseHeaderTimeout: routeCfg.Upstream.Timeout,
    MaxIdleConns:          100,
    MaxIdleConnsPerHost:   10,
    IdleConnTimeout:       90 * time.Second,
}
```

**ErrorHandler:** Logs error, returns 502 JSON.

### Tests: `internal/proxy/proxy_test.go`

| Test | Description |
|---|---|
| `TestProxy_ForwardsRequest` | Upstream receives correct method/path/headers |
| `TestProxy_PathRewrite` | `${id}` substituted correctly |
| `TestProxy_PathRewriteMultipleParams` | Two params both substituted |
| `TestProxy_NoPathRewrite` | Original path forwarded |
| `TestProxy_UpstreamDown_502` | Closed port → 502 JSON |
| `TestProxy_ErrorResponseFormat` | Valid JSON with `error` and `message` |
| `TestProxy_HostHeaderRewrite` | Host matches upstream URL |

---

## Step 7: `cmd/gateway/main.go` — Entry Point

### Phase 1 wiring:
1. Parse `--config` flag
2. Load config via `config.LoadConfig()`
3. Setup slog (JSON handler to stdout, level from config)
4. Build per-route proxy handlers
5. Create router
6. Build global chain: `Recovery → RequestID`
7. Create mux with `"/"` → `globalChain(router)`
8. Start `http.Server` with graceful shutdown (SIGINT/SIGTERM)

### Graceful shutdown:
- `signal.Notify` on buffered channel for SIGINT, SIGTERM
- `srv.Shutdown(ctx)` with configured timeout
- Log shutdown start and completion

---

## Step 8: Integration Test

Test the full request path: mock upstream → config → handlers → router → middleware → proxy.

| Test | Description |
|---|---|
| Successful proxy with path rewrite | `GET /api/users/42` → upstream gets `/internal/users/42` |
| Request ID propagation | Response and upstream both have `X-Request-ID` |
| 404 on unknown path | `GET /nonexistent` → 404 JSON |
| Method filtering | `DELETE` on GET-only route → 404 |
| Panic recovery | Panicking handler → 500 JSON |

---

## Implementation Order

```
Step 0: Project init (go mod, directories)
   │
Step 1: internal/config/config.go + tests
   │
Step 2: internal/middleware/chain.go + tests
   │
   ├── Step 3: internal/middleware/recovery.go + tests (parallel)
   ├── Step 4: internal/middleware/requestid.go + tests (parallel)
   │
Step 5: internal/router/router.go + tests
   │
Step 6: internal/proxy/proxy.go + tests
   │
Step 7: cmd/gateway/main.go
   │
Step 8: Integration test
```

---

## Key Gotchas

1. `gopkg.in/yaml.v3` handles `time.Duration` natively — no custom unmarshalling needed
2. Env var substitution happens on raw text BEFORE YAML parsing — `port: ${PORT}` works
3. `regexp.MustCompile` at startup — fail fast on bad regex
4. One `http.Transport` per route for connection pooling — never per request
5. `req.Host` must be set explicitly in Director
6. Recovery middleware cannot catch goroutine panics — only same-goroutine
7. No 405 in Phase 1 — router returns 404 when no route matches
8. Context key types are unexported — access only through exported functions
9. `json.Encoder.Encode` adds trailing `\n` — use `strings.TrimSpace` in tests if comparing exact bodies
