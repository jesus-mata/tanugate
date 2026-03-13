# API Gateway in Go — Complete Implementation Specification

## Context

Build a stateless API Gateway in Go that sits in front of downstream services, providing routing, authentication, rate limiting, request/response transformation, retries, circuit breaking, CORS, and observability — all driven by a single YAML config file.

**Project**: `~/Documents/SWE/NextSolutions/api-gateway`
**Module**: `github.com/jesus-mata/tanugate`
**Go version**: 1.25+
**Config reload**: No hot reload — config is loaded once at startup
**Deployment**: Standalone binary + Docker container

---

## 1. Project Structure

```
api-gateway/
├── cmd/
│   └── gateway/
│       └── main.go                              # Entry point: load config, build chains, start server
├── internal/
│   ├── config/
│   │   ├── config.go                            # All YAML config structs + LoadConfig() + env var substitution
│   │   └── config_test.go
│   ├── router/
│   │   ├── router.go                            # Regex-based route matcher, stores MatchedRoute in context
│   │   └── router_test.go
│   ├── proxy/
│   │   ├── proxy.go                             # httputil.ReverseProxy wrapper with path rewriting
│   │   └── proxy_test.go
│   ├── middleware/
│   │   ├── chain.go                             # Middleware type definition + Chain() composer function
│   │   ├── chain_test.go
│   │   ├── logging.go                           # slog structured JSON request/response logging
│   │   ├── logging_test.go
│   │   ├── cors.go                              # CORS preflight handling + response header injection
│   │   ├── cors_test.go
│   │   ├── requestid.go                         # X-Request-ID generation (UUID v4) and propagation
│   │   ├── requestid_test.go
│   │   ├── recovery.go                          # Panic recovery → 500 Internal Server Error
│   │   ├── recovery_test.go
│   │   ├── auth/
│   │   │   ├── auth.go                          # Authenticator interface + NewAuthenticator() factory
│   │   │   ├── auth_test.go
│   │   │   ├── jwt.go                           # JWT signature/expiry/claims validation
│   │   │   ├── jwt_test.go
│   │   │   ├── apikey.go                        # API key header validation (constant-time compare)
│   │   │   ├── apikey_test.go
│   │   │   ├── oidc.go                          # OAuth2/OIDC: JWKS fetching/caching + token introspection
│   │   │   └── oidc_test.go
│   │   ├── ratelimit/
│   │   │   ├── ratelimit.go                     # Limiter interface + rate limit middleware + key extractor
│   │   │   ├── ratelimit_test.go
│   │   │   ├── memory.go                        # In-memory token bucket with periodic cleanup goroutine
│   │   │   ├── memory_test.go
│   │   │   ├── redis.go                         # Redis sliding window using Lua script for atomicity
│   │   │   └── redis_test.go
│   │   ├── transform/
│   │   │   ├── transform.go                     # Request/response header + JSON body transforms
│   │   │   └── transform_test.go
│   │   ├── circuitbreaker/
│   │   │   ├── circuitbreaker.go                # Circuit breaker state machine (Closed/Open/HalfOpen)
│   │   │   └── circuitbreaker_test.go
│   │   └── retry/
│   │       ├── retry.go                         # Exponential backoff with jitter
│   │       └── retry_test.go
│   └── observability/
│       ├── health.go                            # GET /health liveness/readiness endpoint
│       ├── health_test.go
│       ├── metrics.go                           # Prometheus /metrics endpoint + metrics middleware
│       └── metrics_test.go
├── config/
│   ├── gateway.yaml                             # Production config template
│   └── gateway.example.yaml                     # Fully annotated reference config
├── Dockerfile                                   # Multi-stage build (golang:alpine → alpine runtime)
├── Makefile                                     # build, test, lint, docker-build, docker-push targets
├── go.mod
├── go.sum
└── gateway.md                                   # This spec document
```

---

## 2. External Dependencies

| Module | Version | Purpose |
|---|---|---|
| `gopkg.in/yaml.v3` | latest | YAML config file parsing |
| `github.com/golang-jwt/jwt/v5` | v5.2+ | JWT token parsing and validation |
| `github.com/redis/go-redis/v9` | v9.7+ | Redis client for distributed rate limiting |
| `github.com/prometheus/client_golang` | v1.20+ | Prometheus metrics registry and HTTP handler |

**Stdlib usage** (no external dependency needed):
- HTTP server + reverse proxy: `net/http`, `net/http/httputil`
- Structured logging: `log/slog` with `slog.NewJSONHandler`
- Regex routing: `regexp`
- JSON body transforms: `encoding/json`
- CORS: custom implementation using `net/http`
- Circuit breaker + retry: custom using `sync`, `time`, `math/rand`
- API key security: `crypto/subtle.ConstantTimeCompare`
- UUID generation: `crypto/rand` + formatting
- Environment variable substitution: `os.Getenv` + `regexp`
- Graceful shutdown: `os/signal`, `context`

---

## 3. YAML Configuration — Complete Reference (`gateway.yaml`)

### 3.1 `server` — HTTP Server Settings

Controls the Go `http.Server` that listens for incoming requests.

```yaml
server:
  host: "0.0.0.0"          # Bind address. Use "0.0.0.0" to listen on all interfaces.
  port: 8080                # TCP port number. Integer, required.
  read_timeout: 30s         # Max time to read the entire request (headers + body). Go duration format.
  write_timeout: 30s        # Max time to write the response. Go duration format.
  idle_timeout: 120s        # Max time to wait for the next request on a keep-alive connection.
  shutdown_timeout: 15s     # Graceful shutdown deadline. In-flight requests get this long to complete.
```

**Go struct:**
```go
type ServerConfig struct {
    Host            string        `yaml:"host"`
    Port            int           `yaml:"port"`
    ReadTimeout     time.Duration `yaml:"read_timeout"`
    WriteTimeout    time.Duration `yaml:"write_timeout"`
    IdleTimeout     time.Duration `yaml:"idle_timeout"`
    ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}
```

**Defaults** (applied if omitted):
| Field | Default |
|---|---|
| `host` | `"0.0.0.0"` |
| `port` | `8080` |
| `read_timeout` | `30s` |
| `write_timeout` | `30s` |
| `idle_timeout` | `120s` |
| `shutdown_timeout` | `15s` |

---

### 3.2 `logging` — Structured Logging

Configures the `slog` JSON logger used across all middleware and the proxy.

```yaml
logging:
  level: "info"             # Minimum log level: "debug" | "info" | "warn" | "error"
```

**Go struct:**
```go
type LoggingConfig struct {
    Level string `yaml:"level"` // Maps to slog.Level
}
```

**Behavior:**
- All logs are emitted as structured JSON to stdout via `slog.NewJSONHandler`.
- Each log entry includes: `time`, `level`, `msg`, and contextual attributes (`method`, `path`, `status`, `latency_ms`, `request_id`, `route`, `remote_addr`).
- At `"debug"` level, request/response headers are logged.
- Default: `"info"`.

---

### 3.3 `cors` — Global CORS Configuration

Default CORS policy applied to all routes. Individual routes can override any field.

```yaml
cors:
  allowed_origins:           # List of allowed origins. Use ["*"] to allow all.
    - "https://app.example.com"
    - "https://admin.example.com"
  allowed_methods:           # HTTP methods allowed in CORS requests.
    - "GET"
    - "POST"
    - "PUT"
    - "DELETE"
    - "PATCH"
    - "OPTIONS"
  allowed_headers:           # Request headers the client is allowed to send.
    - "Authorization"
    - "Content-Type"
    - "X-API-Key"
    - "X-Request-ID"
  exposed_headers:           # Response headers the browser is allowed to read.
    - "X-Request-ID"
    - "X-RateLimit-Remaining"
    - "X-RateLimit-Reset"
  allow_credentials: true    # Whether Access-Control-Allow-Credentials is set.
  max_age: 3600              # Preflight cache duration in seconds (Access-Control-Max-Age).
```

**Go struct:**
```go
type CORSConfig struct {
    AllowedOrigins   []string `yaml:"allowed_origins"`
    AllowedMethods   []string `yaml:"allowed_methods"`
    AllowedHeaders   []string `yaml:"allowed_headers"`
    ExposedHeaders   []string `yaml:"exposed_headers"`
    AllowCredentials bool     `yaml:"allow_credentials"`
    MaxAge           int      `yaml:"max_age"`
}
```

**Behavior:**
- On `OPTIONS` preflight: responds with `204 No Content` and CORS headers. Does NOT forward to upstream.
- On regular requests: injects CORS response headers.
- If a route has its own `cors:` block, those fields **override** (not merge with) the global defaults.
- `allowed_origins: ["*"]` with `allow_credentials: true` is invalid per spec — the middleware should log a warning and set the origin to the requesting origin instead.

---

### 3.4 `rate_limit` — Global Rate Limiting Backend

Selects which rate limiting backend is used for all routes.

```yaml
rate_limit:
  backend: "memory"          # "memory" | "redis"
  redis:                     # Only used when backend is "redis"
    addr: "redis:6379"       # Redis server address (host:port)
    password: ""             # Redis password. Use ${REDIS_PASSWORD} for env var.
    db: 0                    # Redis database number (0-15)
```

**Go struct:**
```go
type RateLimitGlobalConfig struct {
    Backend string      `yaml:"backend"` // "memory" | "redis"
    Redis   RedisConfig `yaml:"redis"`
}

type RedisConfig struct {
    Addr     string `yaml:"addr"`
    Password string `yaml:"password"`
    DB       int    `yaml:"db"`
}
```

**Behavior:**
- `"memory"`: Uses an in-memory token bucket per unique key. State is local to the process — if you run multiple gateway instances, each has its own counters. A background goroutine runs every 60 seconds to evict expired entries.
- `"redis"`: Uses a Redis sorted set with a Lua script for atomic sliding window rate limiting. Correct across multiple gateway instances.
- The `redis` block is ignored when `backend` is `"memory"`.
- Default: `"memory"`.

---

### 3.5 `auth_providers` — Authentication Provider Definitions

Named authentication providers that routes reference by key. Each provider has a `type` and a corresponding config block.

```yaml
auth_providers:
  # --- JWT Provider ---
  jwt_default:
    type: "jwt"
    jwt:
      secret: "${JWT_SECRET}"            # HMAC shared secret (for HS256/HS384/HS512)
      public_key_file: ""                # Path to PEM public key file (for RS256/RS384/RS512/ES256/ES384/ES512)
      algorithm: "HS256"                 # Signing algorithm. Required.
                                         # Supported: HS256, HS384, HS512, RS256, RS384, RS512, ES256, ES384, ES512
      issuer: "https://auth.example.com" # Expected "iss" claim. Empty string = skip validation.
      audience: "my-app"                 # Expected "aud" claim. Empty string = skip validation.

  # --- API Key Provider ---
  apikey_services:
    type: "apikey"
    apikey:
      header: "X-API-Key"               # Which request header contains the API key.
      keys:                              # List of valid API keys.
        - key: "${API_KEY_SVC1}"         # The key value. Always use env vars for secrets.
          name: "billing-service"        # Human-readable label for logging/metrics.
        - key: "${API_KEY_SVC2}"
          name: "notification-service"

  # --- OIDC Provider ---
  oidc_keycloak:
    type: "oidc"
    oidc:
      issuer_url: "http://keycloak:8080/realms/myapp"   # OpenID Connect issuer URL.
                                                         # Used to auto-discover JWKS endpoint via
                                                         # {issuer_url}/.well-known/openid-configuration
      jwks_url: ""                       # Explicit JWKS URL. Overrides auto-discovery if set.
      introspection_url: ""              # RFC 7662 token introspection endpoint. If set, uses introspection
                                         # instead of local JWT validation. Requires client_id + client_secret.
      client_id: "api-gateway"           # OAuth2 client ID (for introspection auth).
      client_secret: "${OIDC_SECRET}"    # OAuth2 client secret (for introspection auth).
      audience: "account"                # Expected "aud" claim.
      cache_ttl: 300s                    # How long to cache the JWKS key set in memory. Go duration format.
```

**Go structs:**
```go
type AuthProvider struct {
    Type   string       `yaml:"type"`   // "jwt" | "apikey" | "oidc"
    JWT    *JWTConfig   `yaml:"jwt,omitempty"`
    APIKey *APIKeyConfig `yaml:"apikey,omitempty"`
    OIDC   *OIDCConfig  `yaml:"oidc,omitempty"`
}

type JWTConfig struct {
    Secret        string `yaml:"secret"`
    PublicKeyFile string `yaml:"public_key_file"`
    Algorithm     string `yaml:"algorithm"`
    Issuer        string `yaml:"issuer"`
    Audience      string `yaml:"audience"`
}

type APIKeyConfig struct {
    Header string       `yaml:"header"`
    Keys   []APIKeyEntry `yaml:"keys"`
}

type APIKeyEntry struct {
    Key  string `yaml:"key"`
    Name string `yaml:"name"`
}

type OIDCConfig struct {
    IssuerURL        string        `yaml:"issuer_url"`
    JWKSURL          string        `yaml:"jwks_url"`
    IntrospectionURL string        `yaml:"introspection_url"`
    ClientID         string        `yaml:"client_id"`
    ClientSecret     string        `yaml:"client_secret"`
    Audience         string        `yaml:"audience"`
    CacheTTL         time.Duration `yaml:"cache_ttl"`
}
```

**Auth implementation details:**

| Provider | How it works |
|---|---|
| `jwt` | Reads `Authorization: Bearer <token>` header. Parses JWT using `golang-jwt/jwt/v5`. Validates signature against `secret` (HMAC) or `public_key_file` (RSA/ECDSA). Checks `exp`, `iss`, `aud` claims. Stores parsed claims in request context for downstream use (e.g., rate limit keying by `claim:sub`). |
| `apikey` | Reads the configured header (e.g., `X-API-Key`). Compares against each key in the list using `crypto/subtle.ConstantTimeCompare` to prevent timing attacks. If matched, stores the key's `name` in context for logging/metrics. If no match, returns `401`. |
| `oidc` | At startup, fetches JWKS from `jwks_url` or auto-discovers via `{issuer_url}/.well-known/openid-configuration`. Caches keys in memory for `cache_ttl`. Validates JWT by resolving signing key from JWKS using the token's `kid` header. If `introspection_url` is set instead, sends RFC 7662 POST to the IdP with the token and checks `active: true` in response. |

**Error responses:**
- Missing/malformed credentials → `401 Unauthorized` with JSON body `{"error": "unauthorized", "message": "..."}`
- Valid credentials but insufficient permissions → `403 Forbidden`

---

### 3.6 `routes` — Route Definitions

The ordered list of routes. The router evaluates routes **in order** and uses the **first match**. Each route maps a URL pattern to an upstream service and configures per-route middleware.

```yaml
routes:
  - name: "route-name"                     # Unique name for logging, metrics, and debugging. Required.

    # --- Matching ---
    match:
      path_regex: "^/api/users/(?P<rest>.*)"   # Go-flavored regex. Named capture groups (?P<name>...)
                                                # are available for path_rewrite. Required.
      methods:                                  # Restrict to specific HTTP methods. Optional.
        - "GET"                                 # If omitted, all methods are allowed.
        - "POST"

    # --- Upstream ---
    upstream:
      url: "http://user-service:8080"      # Base URL of the downstream service. Required.
      path_rewrite: "/api/${rest}"          # Rewrite path using named capture groups from path_regex.
                                            # ${group_name} is replaced with the matched value.
                                            # If omitted, the original request path is forwarded as-is.
      timeout: 10s                          # Per-request timeout for the upstream call. Go duration format.
                                            # Default: 30s.

    # --- Authentication ---
    auth:
      provider: "jwt_default"              # Name of an auth_provider defined above.
                                            # Use "none" to make this route public (no auth).
                                            # Required.

    # --- Rate Limiting (optional) ---
    rate_limit:
      requests_per_window: 100             # Maximum number of requests allowed within the window.
      window: 1m                           # Time window. Go duration format (e.g., 1s, 30s, 1m, 1h).
      key_source: "ip"                     # How to identify the client for rate limiting:
                                            #   "ip"              — Use the client's IP address (X-Forwarded-For or RemoteAddr)
                                            #   "header:<name>"   — Use the value of a specific request header (e.g., "header:X-API-Key")
                                            #   "claim:<name>"    — Use a JWT claim value (e.g., "claim:sub"). Requires JWT auth.

    # --- Retry (optional) ---
    retry:
      max_retries: 3                       # Maximum number of retry attempts (0 = no retries). Default: 0.
      initial_delay: 100ms                 # Delay before the first retry. Go duration format.
      multiplier: 2.0                      # Backoff multiplier. delay = initial_delay * multiplier^attempt.
                                            # Jitter (0.5x-1.5x random factor) is applied automatically.
      retryable_status_codes:              # Only retry when upstream returns one of these status codes.
        - 502                              # If omitted, retries only on network errors (connection refused,
        - 503                              # timeout, DNS failure), NOT on HTTP error responses.
        - 504

    # --- Circuit Breaker (optional) ---
    circuit_breaker:
      failure_threshold: 5                 # Consecutive failures to trigger circuit OPEN. Default: 5.
      success_threshold: 3                 # Consecutive successes in HALF-OPEN to close circuit. Default: 3.
      timeout: 30s                         # How long circuit stays OPEN before transitioning to HALF-OPEN.

    # --- Request/Response Transformation (optional) ---
    transform:
      request:
        headers:
          add:                             # Headers to add/set on the request before forwarding.
            X-Gateway: "api-gateway"       # Static values.
            X-Request-ID: "${request_id}"  # Dynamic variables (see variables table below).
            X-Forwarded-For: "${client_ip}"
          remove:                          # Headers to remove from the request before forwarding.
            - "X-Internal-Debug"
          rename:                          # Headers to rename (old name → new name).
            X-Legacy-Auth: "Authorization"
        body:                              # JSON body transforms (only applied to Content-Type: application/json)
          inject_fields:                   # Fields to add to the top-level JSON object.
            gateway_timestamp: "${timestamp_iso}"
            gateway_version: "1.0.0"
          strip_fields:                    # Fields to remove from the top-level JSON object.
            - "internal_debug"
            - "trace_data"
          rename_keys:                     # Top-level JSON keys to rename (old → new).
            old_field_name: "new_field_name"
      response:
        headers:
          add:
            X-Served-By: "api-gateway"
            X-Response-Time: "${latency_ms}ms"
          remove:
            - "Server"
            - "X-Powered-By"
          rename: {}
        body:                              # Response body transforms (same syntax as request body)
          inject_fields: {}
          strip_fields: []
          rename_keys: {}

    # --- CORS Override (optional) ---
    cors:                                  # Overrides the global CORS config for this route.
      allowed_origins:                     # Only the fields specified here override; the rest
        - "*"                              # fall back to the global cors config.
```

**Go structs:**
```go
type RouteConfig struct {
    Name           string                `yaml:"name"`
    Match          MatchConfig           `yaml:"match"`
    Upstream       UpstreamConfig        `yaml:"upstream"`
    Auth           RouteAuthConfig       `yaml:"auth"`
    RateLimit      *RouteLimitConfig     `yaml:"rate_limit,omitempty"`
    Retry          *RetryConfig          `yaml:"retry,omitempty"`
    CircuitBreaker *CircuitBreakerConfig `yaml:"circuit_breaker,omitempty"`
    Transform      *TransformConfig      `yaml:"transform,omitempty"`
    CORS           *CORSConfig           `yaml:"cors,omitempty"`
}

type MatchConfig struct {
    PathRegex string   `yaml:"path_regex"`
    Methods   []string `yaml:"methods,omitempty"`
}

type UpstreamConfig struct {
    URL         string        `yaml:"url"`
    PathRewrite string        `yaml:"path_rewrite,omitempty"`
    Timeout     time.Duration `yaml:"timeout"`
}

type RouteAuthConfig struct {
    Provider string `yaml:"provider"` // Key into auth_providers map, or "none"
}

type RouteLimitConfig struct {
    RequestsPerWindow int           `yaml:"requests_per_window"`
    Window            time.Duration `yaml:"window"`
    KeySource         string        `yaml:"key_source"`
}

type RetryConfig struct {
    MaxRetries           int           `yaml:"max_retries"`
    InitialDelay         time.Duration `yaml:"initial_delay"`
    Multiplier           float64       `yaml:"multiplier"`
    RetryableStatusCodes []int         `yaml:"retryable_status_codes,omitempty"`
}

type CircuitBreakerConfig struct {
    FailureThreshold int           `yaml:"failure_threshold"`
    SuccessThreshold int           `yaml:"success_threshold"`
    Timeout          time.Duration `yaml:"timeout"`
}

type TransformConfig struct {
    Request  *DirectionTransform `yaml:"request,omitempty"`
    Response *DirectionTransform `yaml:"response,omitempty"`
}

type DirectionTransform struct {
    Headers *HeaderTransform `yaml:"headers,omitempty"`
    Body    *BodyTransform   `yaml:"body,omitempty"`
}

type HeaderTransform struct {
    Add    map[string]string `yaml:"add,omitempty"`
    Remove []string          `yaml:"remove,omitempty"`
    Rename map[string]string `yaml:"rename,omitempty"`
}

type BodyTransform struct {
    InjectFields map[string]string `yaml:"inject_fields,omitempty"`
    StripFields  []string          `yaml:"strip_fields,omitempty"`
    RenameKeys   map[string]string `yaml:"rename_keys,omitempty"`
}
```

**Dynamic variables** available in header `add` values and body `inject_fields`:

| Variable | Description |
|---|---|
| `${request_id}` | UUID v4 generated by the RequestID middleware |
| `${client_ip}` | Client IP from X-Forwarded-For or RemoteAddr |
| `${timestamp_iso}` | Current time in ISO 8601 format |
| `${timestamp_unix}` | Current time as Unix epoch seconds |
| `${latency_ms}` | Response latency in milliseconds (response transforms only) |
| `${route_name}` | Name of the matched route |
| `${method}` | HTTP method of the request |
| `${path}` | Original request path |

---

### 3.7 Environment Variable Substitution

Any value in the YAML config can use `${ENV_VAR_NAME}` syntax. The config loader replaces these with the corresponding environment variable value at load time.

**Rules:**
- `${VAR}` — Replaced with the value of environment variable `VAR`. If `VAR` is not set, the config loader logs a warning and leaves the value as an empty string.
- Substitution happens before YAML parsing of typed fields, so `${PORT}` in `port: ${PORT}` works if `PORT=8080`.
- Only `${...}` syntax is supported (not `$VAR` without braces).

**Implementation:** Use `regexp.MustCompile(`\$\{([^}]+)\}`)` to find all placeholders, then `os.Getenv` to resolve them.

---

## 4. Middleware Chain Architecture

### 4.1 Middleware Type

```go
// internal/middleware/chain.go

// Middleware wraps an http.Handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain composes multiple middleware into one. Applied in order:
// Chain(A, B, C)(handler) executes as: A → B → C → handler → C → B → A
func Chain(middlewares ...Middleware) Middleware {
    return func(final http.Handler) http.Handler {
        for i := len(middlewares) - 1; i >= 0; i-- {
            final = middlewares[i](final)
        }
        return final
    }
}
```

### 4.2 Execution Order

```
Incoming Request
       │
       ▼
┌──────────────────┐
│  1. Recovery     │  Catches panics → 500 + logs stack trace
├──────────────────┤
│  2. RequestID    │  Generates UUID v4, sets X-Request-ID header, stores in context
├──────────────────┤
│  3. Logging      │  Logs request start, defers response log with status + latency
├──────────────────┤
│  4. Metrics      │  Increments request counter, starts latency timer
├──────────────────┤
│  5. CORS         │  Handles OPTIONS preflight → 204. Adds CORS headers to all responses.
├──────────────────┤
│  6. Router       │  Matches path against compiled regex routes (first match wins).
│                  │  No match → 404. Stores MatchedRoute in context.
│                  │  Dispatches to per-route handler chain.
├──────────────────┤
│  Per-Route Chain │
│ ┌────────────────┤
│ │ 7. RateLimit   │  Extracts key, checks limiter → 429 if exceeded
│ ├────────────────┤
│ │ 8. Auth        │  Validates credentials per provider → 401/403 if invalid
│ ├────────────────┤
│ │ 9. ReqTransform│  Modifies request headers and JSON body before forwarding
│ ├────────────────┤
│ │10. CB + Retry  │  Circuit breaker wraps retry loop wraps proxy call
│ ├────────────────┤
│ │11. Proxy       │  httputil.ReverseProxy forwards to upstream, streams response
│ ├────────────────┤
│ │12. ResTransform│  Modifies response headers and JSON body before sending to client
│ └────────────────┤
└──────────────────┘
       │
       ▼
  Response to Client
```

**Global middleware** (1-5) are applied once, wrapping the router. They execute for every request including `/health` and `/metrics`.

**Per-route middleware** (7-12) are assembled per route at startup. Each route gets its own chain based on its YAML config. Only the middleware that are configured for that route are included.

### 4.3 How the Router Dispatches

```go
// internal/router/router.go

type Router struct {
    routes []compiledRoute // Compiled at startup from RouteConfig
}

type compiledRoute struct {
    name    string
    regex   *regexp.Regexp
    methods map[string]bool       // nil means all methods allowed
    handler http.Handler          // The per-route middleware chain
    config  *config.RouteConfig
}

type MatchedRoute struct {
    Config     *config.RouteConfig
    PathParams map[string]string   // Named regex capture groups
}

// contextKey for storing MatchedRoute
type contextKey struct{}

func RouteFromContext(ctx context.Context) *MatchedRoute { ... }

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
    for _, route := range r.routes {
        matches := route.regex.FindStringSubmatch(req.URL.Path)
        if matches == nil {
            continue
        }
        if route.methods != nil && !route.methods[req.Method] {
            continue // Method not allowed for this route
        }
        // Extract named groups
        params := make(map[string]string)
        for i, name := range route.regex.SubexpNames() {
            if i > 0 && name != "" {
                params[name] = matches[i]
            }
        }
        // Store in context and dispatch
        ctx := context.WithValue(req.Context(), contextKey{}, &MatchedRoute{
            Config:     route.config,
            PathParams: params,
        })
        route.handler.ServeHTTP(w, req.WithContext(ctx))
        return
    }
    // No route matched
    http.Error(w, `{"error":"not_found","message":"no matching route"}`, http.StatusNotFound)
}
```

---

## 5. Reverse Proxy

```go
// internal/proxy/proxy.go

func NewProxy() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        matched := router.RouteFromContext(r.Context())
        if matched == nil {
            http.Error(w, "internal error: no route in context", 500)
            return
        }

        target, _ := url.Parse(matched.Config.Upstream.URL)

        proxy := &httputil.ReverseProxy{
            Director: func(req *http.Request) {
                req.URL.Scheme = target.Scheme
                req.URL.Host = target.Host
                req.Host = target.Host

                // Apply path rewrite using named capture groups
                if matched.Config.Upstream.PathRewrite != "" {
                    path := matched.Config.Upstream.PathRewrite
                    for k, v := range matched.PathParams {
                        path = strings.ReplaceAll(path, "${"+k+"}", v)
                    }
                    req.URL.Path = path
                }
            },
            Transport: &http.Transport{
                // Use sensible defaults or configure from server config
                ResponseHeaderTimeout: matched.Config.Upstream.Timeout,
            },
            ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
                slog.Error("proxy error",
                    "route", matched.Config.Name,
                    "upstream", target.String(),
                    "error", err,
                    "request_id", middleware.RequestIDFromContext(r.Context()),
                )
                w.Header().Set("Content-Type", "application/json")
                w.WriteHeader(http.StatusBadGateway)
                json.NewEncoder(w).Encode(map[string]string{
                    "error":   "bad_gateway",
                    "message": "upstream service unavailable",
                })
            },
        }
        proxy.ServeHTTP(w, r)
    })
}
```

**Note:** In practice, the `httputil.ReverseProxy` and `http.Transport` should be created once per route at startup (not per request) for connection pooling. The Director function captures the per-request route info via closure over the context.

---

## 6. Feature Implementation Details

### 6.1 Circuit Breaker

```go
// internal/middleware/circuitbreaker/circuitbreaker.go

type State int
const (
    StateClosed   State = iota  // Normal: requests pass through
    StateOpen                    // Failing: requests rejected immediately
    StateHalfOpen                // Testing: limited requests allowed
)

type CircuitBreaker struct {
    mu               sync.Mutex
    state            State
    failureCount     int
    successCount     int
    failureThreshold int        // From config
    successThreshold int        // From config
    timeout          time.Duration // From config
    lastStateChange  time.Time
}

func (cb *CircuitBreaker) Execute(fn func() error) error {
    cb.mu.Lock()
    // Check state transitions
    if cb.state == StateOpen {
        if time.Since(cb.lastStateChange) > cb.timeout {
            cb.state = StateHalfOpen
            cb.successCount = 0
        } else {
            cb.mu.Unlock()
            return ErrCircuitOpen // → 503
        }
    }
    cb.mu.Unlock()

    err := fn()

    cb.mu.Lock()
    defer cb.mu.Unlock()
    if err != nil {
        cb.failureCount++
        cb.successCount = 0
        if cb.failureCount >= cb.failureThreshold {
            cb.state = StateOpen
            cb.lastStateChange = time.Now()
        }
    } else {
        cb.successCount++
        cb.failureCount = 0
        if cb.state == StateHalfOpen && cb.successCount >= cb.successThreshold {
            cb.state = StateClosed
            cb.lastStateChange = time.Now()
        }
    }
    return err
}
```

One `CircuitBreaker` instance is created per route at startup. Thread-safe via mutex.

### 6.2 Retry with Backoff

```go
// internal/middleware/retry/retry.go

func Retry(cfg *config.RetryConfig, cb *circuitbreaker.CircuitBreaker, proxy http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var lastErr error
        retryableCodes := toSet(cfg.RetryableStatusCodes)

        for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
            if attempt > 0 {
                delay := float64(cfg.InitialDelay) * math.Pow(cfg.Multiplier, float64(attempt-1))
                jitter := 0.5 + rand.Float64() // 0.5x to 1.5x
                time.Sleep(time.Duration(delay * jitter))
            }

            // Capture response using a ResponseRecorder
            rec := httptest.NewRecorder()

            err := cb.Execute(func() error {
                proxy.ServeHTTP(rec, r)
                if retryableCodes[rec.Code] {
                    return fmt.Errorf("retryable status %d", rec.Code)
                }
                return nil
            })

            if err == nil {
                // Copy recorded response to actual writer
                copyResponse(w, rec)
                return
            }
            lastErr = err

            if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
                break // Don't retry if circuit is open
            }
        }

        // All retries exhausted
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusBadGateway)
        json.NewEncoder(w).Encode(map[string]string{
            "error":   "bad_gateway",
            "message": fmt.Sprintf("upstream failed after %d attempts: %v", cfg.MaxRetries+1, lastErr),
        })
    })
}
```

### 6.3 Rate Limiting

**In-memory token bucket:**
```go
// internal/middleware/ratelimit/memory.go

type MemoryLimiter struct {
    buckets sync.Map // key → *bucket
}

type bucket struct {
    mu        sync.Mutex
    tokens    float64
    maxTokens float64
    refillRate float64    // tokens per second
    lastRefill time.Time
}

func (m *MemoryLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, time.Time, error) {
    // Get or create bucket for key
    // Refill tokens based on elapsed time
    // Try to consume one token
    // Return result
}
```

**Redis sliding window (Lua script):**
```lua
-- internal/middleware/ratelimit/redis.go (embedded Lua script)
local key = KEYS[1]
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

-- Remove expired entries
redis.call('ZREMRANGEBYSCORE', key, 0, now - window)

-- Count current entries
local count = redis.call('ZCARD', key)

if count < limit then
    -- Add new entry
    redis.call('ZADD', key, now, now .. '-' .. math.random(1000000))
    redis.call('EXPIRE', key, window / 1000)
    return {1, limit - count - 1, now + window}
else
    return {0, 0, now + window}
end
```

**Rate limit response headers** (set on every response, not just 429):
- `X-RateLimit-Limit: 100`
- `X-RateLimit-Remaining: 42`
- `X-RateLimit-Reset: 1709913600` (Unix timestamp)

**429 response body:**
```json
{
  "error": "rate_limit_exceeded",
  "message": "Rate limit exceeded. Try again in 23 seconds.",
  "retry_after": 23
}
```
Also sets `Retry-After` header.

### 6.4 Request/Response Body Transformation

```go
// internal/middleware/transform/transform.go

func TransformRequestBody(body []byte, cfg *BodyTransform) ([]byte, error) {
    if cfg == nil {
        return body, nil
    }

    var data map[string]any
    if err := json.Unmarshal(body, &data); err != nil {
        return body, nil // Not JSON, pass through unchanged
    }

    // 1. Strip fields
    for _, field := range cfg.StripFields {
        delete(data, field)
    }

    // 2. Rename keys
    for oldKey, newKey := range cfg.RenameKeys {
        if val, ok := data[oldKey]; ok {
            data[newKey] = val
            delete(data, oldKey)
        }
    }

    // 3. Inject fields (after rename so injected fields take precedence)
    for key, val := range cfg.InjectFields {
        data[key] = resolveVariable(val, ctx) // Resolve ${request_id}, ${timestamp_iso}, etc.
    }

    return json.Marshal(data)
}
```

**Important:** Body transforms only apply to `Content-Type: application/json`. Non-JSON bodies are passed through unchanged. After body modification, `Content-Length` header must be updated.

---

## 7. Observability

### 7.1 Health Endpoint

```
GET /health
→ 200 OK
{
  "status": "up",
  "timestamp": "2026-03-08T12:00:00Z",
  "checks": {
    "redis": "up"    // Only present if redis backend is configured
  }
}
```

Registered directly on the mux, bypasses per-route middleware chain.

### 7.2 Prometheus Metrics

Exposed at `GET /metrics` using `promhttp.Handler()`.

**Metrics collected:**

| Metric | Type | Labels | Description |
|---|---|---|---|
| `gateway_requests_total` | Counter | `route`, `method`, `status_code` | Total requests processed |
| `gateway_request_duration_seconds` | Histogram | `route`, `method` | Request latency distribution |
| `gateway_request_size_bytes` | Histogram | `route` | Request body size |
| `gateway_response_size_bytes` | Histogram | `route` | Response body size |
| `gateway_circuit_breaker_state` | Gauge | `route`, `state` | Current circuit breaker state (0/1) |
| `gateway_rate_limit_rejected_total` | Counter | `route` | Requests rejected by rate limiter |
| `gateway_upstream_errors_total` | Counter | `route`, `error_type` | Upstream errors (timeout, connection_refused, etc.) |

---

## 8. Entry Point (`cmd/gateway/main.go`)

```go
func main() {
    configPath := flag.String("config", "config/gateway.yaml", "Path to gateway config file")
    flag.Parse()

    // 1. Load config
    cfg, err := config.LoadConfig(*configPath)
    if err != nil {
        slog.Error("failed to load config", "error", err)
        os.Exit(1)
    }

    // 2. Setup slog
    setupLogger(cfg.Logging)

    // 3. Create rate limiter backend
    var limiter ratelimit.Limiter
    switch cfg.RateLimit.Backend {
    case "redis":
        limiter = ratelimit.NewRedisLimiter(cfg.RateLimit.Redis)
    default:
        limiter = ratelimit.NewMemoryLimiter()
    }

    // 4. Create auth providers
    authenticators := buildAuthenticators(cfg.AuthProviders)

    // 5. Build per-route handlers
    routeHandlers := buildRouteHandlers(cfg.Routes, limiter, authenticators, cfg.CORS)

    // 6. Create router
    r := router.New(cfg.Routes, routeHandlers)

    // 7. Build global middleware chain
    globalChain := middleware.Chain(
        middleware.Recovery(),
        middleware.RequestID(),
        middleware.Logging(),
        observability.Metrics(),
        middleware.CORS(cfg.CORS),
    )

    // 8. Create mux with health/metrics + router
    mux := http.NewServeMux()
    mux.HandleFunc("GET /health", observability.HealthHandler(cfg, limiter))
    mux.Handle("GET /metrics", promhttp.Handler())
    mux.Handle("/", globalChain(r))

    // 9. Start server with graceful shutdown
    srv := &http.Server{
        Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
        Handler:      mux,
        ReadTimeout:  cfg.Server.ReadTimeout,
        WriteTimeout: cfg.Server.WriteTimeout,
        IdleTimeout:  cfg.Server.IdleTimeout,
    }

    go func() {
        slog.Info("gateway starting", "addr", srv.Addr)
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            slog.Error("server error", "error", err)
            os.Exit(1)
        }
    }()

    // Wait for interrupt signal
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit

    slog.Info("shutting down gracefully", "timeout", cfg.Server.ShutdownTimeout)
    ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
    defer cancel()
    if err := srv.Shutdown(ctx); err != nil {
        slog.Error("forced shutdown", "error", err)
    }
    slog.Info("gateway stopped")
}
```

---

## 9. Dockerfile

```dockerfile
# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /gateway ./cmd/gateway

# Runtime stage
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /gateway /usr/local/bin/gateway
COPY config/gateway.yaml /etc/gateway/gateway.yaml
EXPOSE 8080
ENTRYPOINT ["gateway"]
CMD ["-config", "/etc/gateway/gateway.yaml"]
```

**Usage:**
```bash
# Build
docker build -t api-gateway .

# Run with env vars for secrets
docker run -p 8080:8080 \
  -v $(pwd)/config/gateway.yaml:/etc/gateway/gateway.yaml \
  -e JWT_SECRET=my-secret \
  -e API_KEY_SVC1=key123 \
  api-gateway
```

---

## 10. Makefile

```makefile
.PHONY: build test lint run docker-build docker-push clean

BINARY=gateway
MODULE=github.com/jesus-mata/tanugate

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(BINARY) ./cmd/gateway

test:
	go test -v -race -count=1 ./...

lint:
	golangci-lint run ./...

run: build
	./bin/$(BINARY) -config config/gateway.yaml

docker-build:
	docker build -t api-gateway .

docker-push: docker-build
	docker push api-gateway

clean:
	rm -rf bin/
```

---

## 11. Error Response Format

All gateway-generated errors use a consistent JSON format:

```json
{
  "error": "error_code",
  "message": "Human-readable description"
}
```

| Status | Error Code | When |
|---|---|---|
| 400 | `bad_request` | Malformed request that can't be forwarded |
| 401 | `unauthorized` | Missing or invalid credentials |
| 403 | `forbidden` | Valid credentials but insufficient permissions |
| 404 | `not_found` | No route matches the request path |
| 405 | `method_not_allowed` | Route matches path but not the HTTP method |
| 429 | `rate_limit_exceeded` | Rate limit exceeded (includes `retry_after` field) |
| 502 | `bad_gateway` | Upstream returned error or is unreachable |
| 503 | `service_unavailable` | Circuit breaker is open |
| 500 | `internal_error` | Unexpected gateway error (panic recovery) |

---

## 12. Implementation Phases

### Phase 1: Foundation (Skeleton + Config + Routing + Proxy)
**Files:** `go.mod`, `cmd/gateway/main.go`, `internal/config/config.go`, `internal/router/router.go`, `internal/proxy/proxy.go`, `internal/middleware/chain.go`, `internal/middleware/recovery.go`, `internal/middleware/requestid.go`
**Result:** Gateway loads YAML, matches paths via regex, proxies to upstream services with path rewriting.

### Phase 2: Observability + Logging
**Files:** `internal/middleware/logging.go`, `internal/observability/health.go`, `internal/observability/metrics.go`
**Result:** Structured JSON logs, `/health` and `/metrics` endpoints working.

### Phase 3: CORS
**Files:** `internal/middleware/cors.go`
**Result:** Cross-origin requests handled with global + per-route config.

### Phase 4: Authentication
**Files:** `internal/middleware/auth/auth.go`, `jwt.go`, `apikey.go`, `oidc.go`
**Result:** Routes protected with JWT, API key, or OIDC authentication.

### Phase 5: Rate Limiting
**Files:** `internal/middleware/ratelimit/ratelimit.go`, `memory.go`, `redis.go`
**Result:** Per-route rate limiting with in-memory or Redis backend.

### Phase 6: Resilience
**Files:** `internal/middleware/retry/retry.go`, `internal/middleware/circuitbreaker/circuitbreaker.go`
**Result:** Retries with exponential backoff + circuit breaker protection.

### Phase 7: Transformation
**Files:** `internal/middleware/transform/transform.go`
**Result:** Request/response header and JSON body transforms.

### Phase 8: Deployment + Polish
**Files:** `Dockerfile`, `Makefile`, `config/gateway.example.yaml`, unit tests for all packages
**Result:** Production-ready container image with annotated example config.

---

## 13. Verification Plan

1. **Unit tests** for each package: config parsing, regex routing, auth validation, rate limiting, circuit breaker state machine, body transforms
2. **Integration test**: `httptest.Server` as mock upstream, full gateway startup with test config, verify:
   - Request forwarding + path rewrite
   - Auth rejection (bad JWT → 401, bad API key → 401, no auth on public route → 200)
   - Rate limit (send N+1 requests → last one gets 429)
   - Circuit breaker (fail upstream 5 times → 503 on 6th → wait timeout → half-open)
   - Header transforms (added headers present, removed headers absent)
   - Body transforms (fields injected, stripped, renamed)
   - CORS headers on responses and OPTIONS preflight
   - `/health` → 200, `/metrics` → Prometheus text format
   - Unknown path → 404
3. **Manual testing**: `curl` commands against running gateway
4. **Docker**: `docker build` + `docker run` with mounted config + env vars
