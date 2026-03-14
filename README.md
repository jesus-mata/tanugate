<p align="center">
  <img src="assets/Gemini_Generated_Image_9eb1of9eb1of9eb1.png" alt="Tanugate Logo" width="400">
</p>

<h1 align="center">Tanugate</h1>

<p align="center">
  A lightweight, stateless, cloud-native API gateway written in Go.
</p>

<p align="center">
  <a href="https://github.com/jesus-mata/tanugate/actions/workflows/ci.yml"><img src="https://github.com/jesus-mata/tanugate/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://codecov.io/gh/jesus-mata/tanugate"><img src="https://codecov.io/gh/jesus-mata/tanugate/branch/main/graph/badge.svg" alt="Coverage"></a>
</p>

<p align="center">
  <a href="#features">Features</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#configuration">Configuration</a> &middot;
  <a href="#deployment">Deployment</a> &middot;
  <a href="#roadmap">Roadmap</a>
</p>

---

Tanugate is an API gateway designed to sit between your clients and backend services, handling authentication, rate limiting, request routing, and more. It is built to run on container orchestrators like **Docker Swarm** and **Kubernetes** and delegates load balancing to the platform.

## Features

### Routing

- **Regex-based path matching** with named capture groups (`(?P<name>...)`)
- **Method filtering** per route (optional — all methods allowed if omitted)
- **Path rewriting** with parameter substitution from captured groups
- **First-match routing** — routes are evaluated in configuration order

```yaml
routes:
  - name: "user-service"
    match:
      path_regex: "^/api/users(?P<path>/.*)?$"
      methods: ["GET", "POST", "PUT", "DELETE"]
    upstream:
      url: "http://users-service:8080"
      path_rewrite: "{path}"
      timeout: 10s
```

### Authentication

Multiple auth providers can be configured globally and assigned per route. Routes can reference one or more providers — the first successful authentication wins.

**JWT**

Supports HMAC (`HS256`, `HS384`, `HS512`), RSA (`RS256`, `RS384`, `RS512`), and ECDSA (`ES256`, `ES384`, `ES512`). Validates signature, expiration, issuer, and audience.

```yaml
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      algorithm: "RS256"
      public_key_file: "/etc/gateway/public.pem"
      issuer: "https://auth.example.com"
      audience: "my-api"
```

**OIDC**

Supports three modes: full discovery via `issuer_url`, direct `jwks_url`, or token `introspection_url`. Automatically refreshes remote key sets in the background.

```yaml
auth_providers:
  my-oidc:
    type: "oidc"
    oidc:
      issuer_url: "https://auth.example.com"
      audience: "my-api"
      allowed_algorithms: ["RS256", "EdDSA"]
```

**API Key**

Constant-time key comparison. Configurable header name (default: `X-API-Key`).

```yaml
auth_providers:
  api_keys:
    type: "apikey"
    api_key:
      header: "X-API-Key"
      keys:
        - key: "${API_KEY_CLIENT_A}"
          name: "client-a"
```

Assign providers to routes:

```yaml
routes:
  - name: "protected"
    auth:
      providers: ["jwt_default", "api_keys"]  # tried in order
    # ...

  - name: "public"
    auth:
      providers: ["none"]  # no authentication
    # ...
```

### Rate Limiting

Per-route rate limiting with two backends:

| Backend | Algorithm | Use Case |
|---------|-----------|----------|
| `memory` | Token bucket | Single instance or development |
| `redis` | Sliding window (Lua) | Multi-instance / production |

Key extraction options:
- `ip` (default) — client IP with trusted proxy support via `X-Forwarded-For`
- `header:<name>` — extract key from a request header

Response headers on every request:

```
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 42
X-RateLimit-Reset: 1710345600
Retry-After: 30          # only on 429
```

```yaml
rate_limit:
  backend: "redis"
  redis:
    addr: "${REDIS_ADDR}"
    password: "${REDIS_PASSWORD}"
    db: 0

routes:
  - name: "api"
    rate_limit:
      requests_per_window: 100
      window: 1m
      key_source: "ip"
```

### Circuit Breaker

Per-route circuit breaker with three states:

- **Closed** — requests flow normally; consecutive failures are counted
- **Open** — requests are rejected immediately; transitions to half-open after timeout
- **Half-Open** — a single probe request is allowed; success closes, failure re-opens

State changes are reported via Prometheus metrics.

```yaml
routes:
  - name: "backend"
    circuit_breaker:
      failure_threshold: 5
      success_threshold: 2
      timeout: 30s
```

### Retry

Exponential backoff with jitter. Integrates with the circuit breaker — retries are attempted when the circuit opens and when upstream returns retryable status codes. Request bodies up to 10 MB are buffered for safe replay.

```yaml
routes:
  - name: "backend"
    retry:
      max_retries: 3
      initial_delay: 100ms
      multiplier: 2.0
      retryable_status_codes: [502, 503, 504]
```

### Request and Response Transforms

Modify headers and JSON bodies on the way in and out. Dynamic variables are interpolated at runtime.

| Variable | Description |
|----------|-------------|
| `${request_id}` | Request ID (generated or from header) |
| `${client_ip}` | Client IP address |
| `${timestamp_iso}` | Current time (RFC 3339) |
| `${timestamp_unix}` | Current Unix timestamp |
| `${latency_ms}` | Request latency (response transforms only) |
| `${route_name}` | Matched route name |
| `${method}` | HTTP method |
| `${path}` | Request URL path |

```yaml
routes:
  - name: "api"
    transform:
      request:
        headers:
          add:
            X-Gateway-Time: "${timestamp_iso}"
          remove: ["X-Internal-Header"]
          rename:
            X-Old-Name: "X-New-Name"
        body:
          inject_fields:
            request_id: "${request_id}"
          strip_fields: ["password"]
      response:
        headers:
          add:
            X-Response-Time: "${latency_ms}ms"
        body:
          strip_fields: ["internal_data"]
```

### CORS

Global CORS configuration with per-route overrides. Handles preflight `OPTIONS` requests automatically.

```yaml
cors:
  allowed_origins: ["https://app.example.com"]
  allowed_methods: ["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"]
  allowed_headers: ["Authorization", "Content-Type", "X-API-Key"]
  exposed_headers: ["X-Request-ID", "X-RateLimit-Remaining"]
  allow_credentials: true
  max_age: 3600
```

### Observability

**Structured Logging** — JSON logs via Go's `slog` to stdout. Every request is logged with method, path, status, latency, request ID, and route name. Debug level includes request/response headers.

**Request ID** — UUID v4 generated per request (or forwarded from `X-Request-ID` header). Injected into logs and response headers.

**Prometheus Metrics** — exposed at `GET /metrics`:

| Metric | Type | Labels |
|--------|------|--------|
| `gateway_requests_total` | Counter | route, method, status_code |
| `gateway_request_duration_seconds` | Histogram | route, method |
| `gateway_request_size_bytes` | Histogram | route |
| `gateway_response_size_bytes` | Histogram | route |
| `gateway_circuit_breaker_state` | Gauge | route, state |
| `gateway_rate_limit_rejected_total` | Counter | route |
| `gateway_upstream_errors_total` | Counter | route, error_type |

**Health Check** — `GET /health` returns system status with dependency checks:

```json
{
  "status": "up",
  "timestamp": "2026-03-13T12:00:00Z",
  "checks": {
    "redis": "up"
  }
}
```

### Graceful Shutdown

Handles `SIGINT` and `SIGTERM`. Stops accepting new connections, drains in-flight requests within a configurable timeout (default 15s), and cleans up resources (Redis connections, OIDC background goroutines, rate limiter cleanup).

### Hot Reload

Reload configuration via `SIGHUP` without restarting the gateway. Routes, rate limits, CORS, transforms, circuit breaker thresholds, retry settings, and auth provider assignments are reloaded atomically. In-flight requests complete with the old configuration while new requests use the updated one.

```bash
# Edit config, then:
kill -HUP $(pgrep gateway)
```

Non-reloadable fields (server host/port, rate limit backend, auth provider definitions) are detected and logged as warnings. Invalid configurations are rejected — the old config continues serving. See [docs/hot-reload.md](docs/hot-reload.md) for details.

### Panic Recovery

Global recovery middleware catches panics in the handler chain, logs the stack trace, and returns a `500 Internal Server Error` instead of crashing the process.

## Quick Start

### Prerequisites

- Go 1.25+
- (Optional) Redis for distributed rate limiting

### Build and Run

```bash
# Clone
git clone https://github.com/jesus-mata/tanugate.git
cd tanugate

# Build
make build

# Run
./bin/gateway -config config/gateway.yaml
```

### Docker

```bash
# Build image
make docker-build

# Run
docker run -p 8080:8080 \
  -v $(pwd)/config/gateway.yaml:/etc/gateway/gateway.yaml \
  -e JWT_SECRET=your-secret \
  -e REDIS_ADDR=redis:6379 \
  api-gateway
```

The Docker image uses a multi-stage build with a minimal Alpine runtime, runs as a non-root user, and produces a statically linked binary.

## Configuration

Tanugate is configured via a single YAML file. All values support environment variable substitution with `${VAR_NAME}` syntax.

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  shutdown_timeout: 15s
  trusted_proxies:
    - "10.0.0.0/8"
    - "172.16.0.0/12"

logging:
  level: "info"    # debug | info | warn | error

cors:
  allowed_origins: ["${CORS_ALLOWED_ORIGIN}"]
  allowed_methods: ["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"]
  allowed_headers: ["Authorization", "Content-Type", "X-API-Key", "X-Request-ID"]
  exposed_headers: ["X-Request-ID", "X-RateLimit-Remaining", "X-RateLimit-Reset"]
  allow_credentials: true
  max_age: 3600

rate_limit:
  backend: "memory"    # memory | redis
  redis:
    addr: "${REDIS_ADDR}"
    password: "${REDIS_PASSWORD}"
    db: 0

auth_providers:
  # ... provider definitions

routes:
  # ... route definitions
```

See `config/gateway.yaml` for a complete example.

## Deployment

### Architecture

Tanugate is designed to be **stateless** and **horizontally scalable**. It does not perform load balancing — that responsibility belongs to the container orchestration platform.

```
Client (HTTPS)
    |
Reverse Proxy / Ingress (TLS termination)
    |
Tanugate (HTTP, port 8080)
    |
Upstream Services (resolved via platform DNS)
```

Tanugate should run **behind a reverse proxy** that handles TLS termination (e.g., Traefik, NGINX Ingress, cloud load balancer). It listens on plain HTTP and should not be exposed directly to the internet.

### Docker Swarm

Upstream URLs use Swarm service names. Swarm's internal DNS and VIP load-balance traffic across replicas automatically.

```yaml
# docker-compose.yml (stack deploy)
services:
  gateway:
    image: ghcr.io/jesus-mata/tanugate:latest
    ports:
      - "8080:8080"
    configs:
      - source: gateway-config
        target: /etc/gateway/gateway.yaml
    environment:
      - REDIS_ADDR=redis:6379
    deploy:
      replicas: 2
    networks:
      - backend

  redis:
    image: redis:7-alpine
    networks:
      - backend

  users-service:
    image: users-service:latest
    deploy:
      replicas: 3
    networks:
      - backend

configs:
  gateway-config:
    file: ./config/gateway.yaml

networks:
  backend:
    driver: overlay
```

Scaling upstream services (`docker service scale users-service=10`) requires no gateway changes.

### Kubernetes

The same principles apply. Use Kubernetes Service names as upstream URLs — `kube-proxy` handles load balancing across pods.

```yaml
# Deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tanugate
spec:
  replicas: 2
  selector:
    matchLabels:
      app: tanugate
  template:
    metadata:
      labels:
        app: tanugate
    spec:
      containers:
        - name: tanugate
          image: ghcr.io/jesus-mata/tanugate:latest
          ports:
            - containerPort: 8080
          env:
            - name: REDIS_ADDR
              value: "redis:6379"
          volumeMounts:
            - name: config
              mountPath: /etc/gateway
      volumes:
        - name: config
          configMap:
            name: tanugate-config
---
# Service
apiVersion: v1
kind: Service
metadata:
  name: tanugate
spec:
  selector:
    app: tanugate
  ports:
    - port: 80
      targetPort: 8080
```

### Environment Variables

All sensitive values should be injected via environment variables:

| Variable | Description |
|----------|-------------|
| `JWT_SECRET` | JWT HMAC signing secret |
| `REDIS_ADDR` | Redis address (`host:port`) |
| `REDIS_PASSWORD` | Redis password |
| `CORS_ALLOWED_ORIGIN` | Allowed CORS origin |
| `RATE_LIMIT_BACKEND` | Rate limit backend (`memory` or `redis`) |

## Development

```bash
make build       # Build binary to bin/gateway
make test        # Run tests with race detection
make lint        # Run golangci-lint
make vet         # Run go vet
make fmt         # Format code
make run         # Build and run with default config
make clean       # Remove build artifacts
```

## Roadmap

- [ ] **Liveness and readiness probes** — Separate `/livez` and `/readyz` endpoints for Kubernetes orchestration ([#15](https://github.com/jesus-mata/tanugate/issues/15))
- [ ] **CI/CD pipeline** — GitHub Actions for lint, test, build, and container image publishing ([#16](https://github.com/jesus-mata/tanugate/issues/16))
- [ ] **OpenTelemetry tracing** — Distributed tracing with W3C `traceparent` propagation and OTLP export ([#17](https://github.com/jesus-mata/tanugate/issues/17))
- [ ] **Forward auth claims to upstreams** — Inject authenticated identity (sub, email, roles) as headers to upstream services ([#18](https://github.com/jesus-mata/tanugate/issues/18))
- [ ] **Config validation CLI** — `gateway validate` subcommand for pre-deployment config checks ([#19](https://github.com/jesus-mata/tanugate/issues/19))
- [x] **Hot reload** — Reload configuration via `SIGHUP` without restarting the gateway ([#20](https://github.com/jesus-mata/tanugate/issues/20))
- [ ] **Request body size limits** — Per-route and global max request body enforcement ([#21](https://github.com/jesus-mata/tanugate/issues/21))
- [ ] **Helm chart** — Packaged Kubernetes deployment

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
