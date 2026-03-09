# Phase 2: Observability + Logging — Implementation Plan

## Overview

Phase 2 adds structured JSON logging, a `/health` liveness endpoint, and Prometheus metrics to the gateway. It introduces one external dependency (`prometheus/client_golang`) and creates a shared `ResponseWriter` wrapper used by both logging and metrics.

**Depends on:** Phase 1 (middleware chain, config structs, router context, requestid)

---

## 1. External Dependency

```bash
go get github.com/prometheus/client_golang@v1.20.0
```

---

## 2. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `internal/middleware/responsewriter.go` | Create | Shared `ResponseWriter` wrapper (status code + bytes capture) |
| `internal/middleware/logging.go` | Create | Structured JSON logging middleware |
| `internal/middleware/logging_test.go` | Create | 9 test cases |
| `internal/observability/health.go` | Create | `GET /health` endpoint with `HealthChecker` interface |
| `internal/observability/health_test.go` | Create | 6 test cases |
| `internal/observability/metrics.go` | Create | Prometheus metrics collector + middleware |
| `internal/observability/metrics_test.go` | Create | 10 test cases |
| `cmd/gateway/main.go` | Modify | Wire logging, metrics, health, /metrics endpoint |

---

## 3. Implementation Details

### 3.1 `internal/middleware/responsewriter.go`

Shared `ResponseWriter` wrapper with:
- `StatusCode() int` — intercepted status code
- `BytesWritten() int` — accumulated response bytes
- `WriteHeader(code)` — idempotent (first call wins)
- `Write(b)` — implicit 200 if WriteHeader not called
- `Flush()` — delegates if underlying writer supports `http.Flusher`
- `Unwrap()` — for `http.ResponseController` compatibility

Idempotent constructor: `WrapResponseWriter(w)` returns existing wrapper if already wrapped.

### 3.2 `internal/middleware/logging.go`

**Position in chain:** Recovery → RequestID → **Logging** → Metrics → ...

```go
func Logging(logger *slog.Logger) Middleware
```

**On each request:**
1. Capture `start := time.Now()`
2. Wrap writer with `WrapResponseWriter`
3. Log request start at Info level: `method`, `path`, `remote_addr`, `request_id`
4. At Debug level: log request headers
5. Call `next.ServeHTTP`
6. Log completion: `method`, `path`, `status`, `latency_ms`, `request_id`, `route`, `remote_addr`, `response_size`
7. At Debug level: log response headers

**Edge cases:**
- Status code 0 (never written) defaults to 200
- Route is `"unknown"` for `/health`, `/metrics` (no matched route)

### 3.3 `internal/observability/health.go`

```go
type HealthChecker interface {
    HealthCheck(ctx context.Context) error
}

type HealthResponse struct {
    Status    string            `json:"status"`
    Timestamp string            `json:"timestamp"`
    Checks    map[string]string `json:"checks,omitempty"`
}

func HealthHandler(cfg *config.GatewayConfig, checker HealthChecker) http.HandlerFunc
```

**Behavior:**
- Always returns 200 (liveness probe)
- `status: "up"` or `"degraded"` if Redis is down
- Redis check only present if `rate_limit.backend == "redis"`
- 2-second timeout on health checks
- Registered directly on mux, bypasses middleware chain

### 3.4 `internal/observability/metrics.go`

```go
type MetricsCollector struct {
    RequestsTotal       *prometheus.CounterVec
    RequestDuration     *prometheus.HistogramVec
    RequestSize         *prometheus.HistogramVec
    ResponseSize        *prometheus.HistogramVec
    CircuitBreakerState *prometheus.GaugeVec
    RateLimitRejected   *prometheus.CounterVec
    UpstreamErrors      *prometheus.CounterVec
}

func NewMetricsCollector(reg prometheus.Registerer) *MetricsCollector
func (m *MetricsCollector) Middleware() middleware.Middleware
```

**Metrics registered:**

| Metric | Type | Labels |
|---|---|---|
| `gateway_requests_total` | Counter | `route`, `method`, `status_code` |
| `gateway_request_duration_seconds` | Histogram | `route`, `method` |
| `gateway_request_size_bytes` | Histogram | `route` |
| `gateway_response_size_bytes` | Histogram | `route` |
| `gateway_circuit_breaker_state` | Gauge | `route`, `state` |
| `gateway_rate_limit_rejected_total` | Counter | `route` |
| `gateway_upstream_errors_total` | Counter | `route`, `error_type` |

CB, rate limit, and upstream error metrics registered now but populated in Phases 5-6.

**Edge case:** `r.ContentLength == -1` (chunked) → skip request size observation.

### 3.5 `cmd/gateway/main.go` Changes

```go
logger := setupLogger(cfg.Logging)

registry := prometheus.NewRegistry()
registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
registry.MustRegister(prometheus.NewGoCollector())
metrics := observability.NewMetricsCollector(registry)

globalChain := middleware.Chain(
    middleware.Recovery(),
    middleware.RequestID(),
    middleware.Logging(logger),
    metrics.Middleware(),
)

mux.HandleFunc("GET /health", observability.HealthHandler(cfg, nil))
mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
mux.Handle("/", globalChain(r))
```

---

## 4. Test Plans

### `logging_test.go`

| Test | Description |
|---|---|
| `TestLogging_BasicFields` | All required fields present in JSON log |
| `TestLogging_RouteField` | Route name from context appears in log |
| `TestLogging_UnknownRoute` | No route context → `route: "unknown"` |
| `TestLogging_StatusCodeCapture` | Handler writes 404 → log shows 404 |
| `TestLogging_DebugHeaders` | Debug level logs request/response headers |
| `TestLogging_InfoNoHeaders` | Info level does NOT log headers |
| `TestLogging_LatencyIsPositive` | `latency_ms >= 0` |
| `TestLogging_ImplicitStatusOK` | Body-only write → status logged as 200 |
| `TestLogging_ResponseSize` | Known body → correct `response_size` |

### `health_test.go`

| Test | Description |
|---|---|
| `TestHealth_BasicResponse` | 200, `status: "up"`, valid timestamp |
| `TestHealth_RedisNotConfigured` | Redis backend + nil checker → `"not_configured"` |
| `TestHealth_RedisUp` | Mock checker returns nil → `"up"` |
| `TestHealth_RedisDown` | Mock checker returns error → `"down"`, `status: "degraded"` |
| `TestHealth_ContentType` | `Content-Type: application/json` |
| `TestHealth_NoChecksField` | Memory backend → no `checks` in JSON |

### `metrics_test.go`

| Test | Description |
|---|---|
| `TestMetrics_RequestCounter` | 3 requests → counter = 3 |
| `TestMetrics_DurationHistogram` | 50ms sleep → sum >= 0.05 |
| `TestMetrics_RequestSizeRecorded` | Known body → observation recorded |
| `TestMetrics_ResponseSizeRecorded` | Known response → observation recorded |
| `TestMetrics_UnknownContentLength` | ContentLength=-1 → no observation |
| `TestMetrics_RouteLabel` | Route name from context |
| `TestMetrics_UnknownRouteLabel` | No route → `"unknown"` |
| `TestMetrics_StatusCodeLabel` | 201 → label `"201"` |
| `TestMetrics_Endpoint` | `/metrics` returns Prometheus text format |
| `TestMetrics_AllDescriptorsRegistered` | All 7 metrics present in registry |

Each test uses its own `prometheus.NewRegistry()` for isolation.

---

## 5. Implementation Sequence

```
Step 1: internal/middleware/responsewriter.go (shared, no deps)
Step 2: internal/middleware/logging.go (depends on responsewriter)
Step 3: internal/observability/health.go (standalone)
Step 4: internal/observability/metrics.go (depends on responsewriter, prometheus)
Step 5: cmd/gateway/main.go modifications
Step 6: All test files (parallel with steps 2-4)
```

Steps 2, 3, 4 are independent and can be implemented in parallel.

---

## 6. Potential Challenges

| Challenge | Mitigation |
|---|---|
| Double wrapping of ResponseWriter | Idempotent `WrapResponseWriter()` returns existing wrapper |
| Route label unknown at middleware time | Use `defer` to log/record after handler returns; router has set context by then |
| Circular import (observability ↔ middleware) | One-way: observability imports middleware, never reverse |
| Test isolation with Prometheus | Each test creates own `prometheus.NewRegistry()` |
| `r.ContentLength == -1` | Guard with `if >= 0` before observing |
