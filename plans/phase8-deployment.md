# Phase 8: Deployment + Polish — Implementation Plan

## Overview

Phase 8 transforms the gateway into a production-ready artifact: Dockerfile, Makefile, config templates, comprehensive integration tests, and a polish pass across all code.

**Depends on:** ALL previous phases (1-7)

---

## 1. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `Dockerfile` | Create | Multi-stage build (golang:1.25-alpine → alpine:3.21) |
| `Makefile` | Create | build, test, lint, run, docker-build, docker-push, clean |
| `config/gateway.yaml` | Create | Production config with env var placeholders |
| `config/gateway.example.yaml` | Create | Fully annotated reference config (~250 lines) |
| `internal/integration_test.go` | Create | Comprehensive end-to-end test (~600-800 lines) |
| `.dockerignore` | Create | Exclude .git, bin/, *.md, plans/ |
| Various existing files | Modify | Error consistency, log review, resource cleanup |

---

## 2. Dockerfile

```dockerfile
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /gateway ./cmd/gateway

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /gateway /usr/local/bin/gateway
COPY config/gateway.yaml /etc/gateway/gateway.yaml
EXPOSE 8080
ENTRYPOINT ["gateway"]
CMD ["-config", "/etc/gateway/gateway.yaml"]
```

**Polish:** Add non-root user for production hardening:
```dockerfile
RUN adduser -D -u 1000 gateway
USER gateway
```

---

## 3. Makefile

```makefile
.PHONY: build test lint run docker-build docker-push clean

BINARY=gateway

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

## 4. Production Config (`config/gateway.yaml`)

Minimal template with env var placeholders for all secrets. Routes left empty (deployment-specific):

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  shutdown_timeout: 15s

logging:
  level: "info"

cors:
  allowed_origins: ["${CORS_ALLOWED_ORIGIN}"]
  allowed_methods: ["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"]
  allowed_headers: ["Authorization", "Content-Type", "X-API-Key", "X-Request-ID"]
  exposed_headers: ["X-Request-ID", "X-RateLimit-Remaining", "X-RateLimit-Reset"]
  allow_credentials: true
  max_age: 3600

rate_limit:
  backend: "${RATE_LIMIT_BACKEND}"
  redis:
    addr: "${REDIS_ADDR}"
    password: "${REDIS_PASSWORD}"
    db: 0

auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "${JWT_SECRET}"
      algorithm: "HS256"
      issuer: "${JWT_ISSUER}"
      audience: "${JWT_AUDIENCE}"

routes: []
```

---

## 5. Example Config (`config/gateway.example.yaml`)

Fully annotated reference (~250 lines) with:
- Every field documented with inline comments
- All three auth provider types with examples
- Multiple example routes demonstrating different feature combinations:
  - Public route (no auth, no rate limit)
  - JWT-protected with rate limiting + retries + circuit breaker + transforms
  - API-key-protected with body transforms
  - OIDC-protected with CORS override
- Dynamic variables table as comments
- Memory vs Redis tradeoffs explained

---

## 6. Integration Test (`internal/integration_test.go`)

### Test Infrastructure

```go
type mockUpstream struct {
    mu         sync.Mutex
    failCount  int
    panicOnPath string
    requestLog []capturedRequest
}
```

Mock upstream echoes request details, configurable to return errors or panic.

### Test Config Routes

1. `test-public` — `^/public/(.*)` — no auth, basic forwarding
2. `test-jwt` — `^/api/jwt/(?P<rest>.*)` — JWT + rate limit + transforms
3. `test-apikey` — `^/api/key/(.*)` — API key + body transforms
4. `test-resilience` — `^/api/resilience/(.*)` — retry + circuit breaker
5. `test-cors-override` — `^/api/cors/(.*)` — per-route CORS

### Test Cases (22+)

| Sub-test | Verifies |
|---|---|
| `RequestForwarding` | Basic proxy + path rewrite |
| `AuthJWTValid` | Valid JWT passes |
| `AuthJWTInvalid` | Bad JWT → 401 |
| `AuthJWTMissing` | No header → 401 |
| `AuthAPIKeyValid` | Valid key passes |
| `AuthAPIKeyInvalid` | Wrong key → 401 |
| `AuthPublicRoute` | No auth needed |
| `RateLimitEnforced` | N+1 requests → 429 |
| `RateLimitHeaders` | X-RateLimit-* present |
| `CircuitBreakerOpens` | N failures → 503 |
| `CircuitBreakerHalfOpen` | Timeout → allows request |
| `CircuitBreakerCloses` | N successes → closed |
| `HeaderTransformAdd` | Upstream gets added headers |
| `HeaderTransformRemove` | Upstream doesn't get removed headers |
| `BodyTransformInject` | JSON field added |
| `BodyTransformStrip` | JSON field removed |
| `BodyTransformRename` | JSON key renamed |
| `CORSPreflightResponse` | OPTIONS → 204 + headers |
| `CORSRegularResponse` | CORS headers on GET |
| `HealthEndpoint` | `/health` → 200 JSON |
| `MetricsEndpoint` | `/metrics` → Prometheus format |
| `UnknownPath404` | No match → 404 JSON |
| `PanicRecovery` | Panic → 500, server survives |
| `RequestIDPresent` | UUID on all responses |
| `ResponseTransformHeaders` | Response headers modified |

### JWT Helper
```go
func generateTestJWT(secret string, claims jwt.MapClaims) string
```

---

## 7. Polish Pass

### 7a. Error Message Consistency

Audit all error response sites. Extract shared helper:
```go
func WriteJSONError(w http.ResponseWriter, statusCode int, errorCode, message string)
```

Files to audit: router.go, proxy.go, recovery.go, auth/*.go, ratelimit.go, retry.go, circuitbreaker usage.

### 7b. Log Message Consistency

- All messages lowercase first letter
- Include `request_id` where available
- Include `route` where matched
- Include `error` attribute on error logs
- No sensitive data (tokens, keys, passwords) at any level

### 7c. Prometheus Metrics Audit

Verify all 7 metrics registered with correct labels. `route` label uses config name, not regex.

### 7d. Resource Leak Check

1. Memory rate limiter cleanup goroutine stoppable via context
2. Per-route `http.Transport` connection pool closed on shutdown
3. Redis connection pool closed on shutdown
4. All `http.Response.Body` closed, including error paths
5. OIDC JWKS refresh goroutine stoppable

### 7e. Graceful Shutdown

1. Catch SIGINT/SIGTERM
2. `srv.Shutdown(ctx)` with configured timeout
3. Close rate limiter resources
4. Close transport connection pools
5. Log shutdown start and completion

---

## 8. Docker Testing

```bash
docker build -t api-gateway .
docker run -p 8080:8080 \
  -e JWT_SECRET=test-secret \
  -e API_KEY_SVC1=test-key \
  -e CORS_ALLOWED_ORIGIN="*" \
  -e RATE_LIMIT_BACKEND=memory \
  api-gateway
curl -s http://localhost:8080/health | jq .
curl -s http://localhost:8080/metrics | head -20
```

---

## 9. Implementation Sequence

```
Step 1: Polish pass (error consistency, logs, metrics, resources)
Steps 2-4: Dockerfile, Makefile, configs (parallel)
Step 5: Integration tests (after polish, validates everything)
Step 6: Docker testing (final manual validation)
```

---

## 10. Acceptance Criteria

- [ ] `make build` produces working binary
- [ ] `make test` passes all unit + integration tests
- [ ] `make lint` passes with no warnings
- [ ] `docker build` succeeds and produces runnable image
- [ ] `docker run` starts gateway, `/health` responds
- [ ] `config/gateway.example.yaml` documents every field
- [ ] All errors follow `{"error":"...", "message":"..."}` format
- [ ] All logs include `request_id` and `route` where applicable
- [ ] All 7 Prometheus metrics emitted with correct labels
- [ ] Graceful shutdown completes in-flight requests, releases resources
- [ ] Integration test covers 22+ test cases
- [ ] No goroutine or connection leaks
