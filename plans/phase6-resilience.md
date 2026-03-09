# Phase 6: Resilience (Circuit Breaker + Retry) — Implementation Plan

## Overview

Phase 6 adds resilience through a circuit breaker state machine (Closed/Open/HalfOpen) and retry with exponential backoff + jitter. These wrap the proxy at position 10 in the per-route chain.

**Depends on:** Phase 1 (config structs, middleware chain, proxy, router context)
**Can run in parallel with:** Phases 3, 4, 5, 7
**No external dependencies** — stdlib only

---

## 1. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `internal/middleware/circuitbreaker/circuitbreaker.go` | Create | State machine with Execute() method |
| `internal/middleware/circuitbreaker/circuitbreaker_test.go` | Create | 11 test cases |
| `internal/middleware/retry/retry.go` | Create | Retry handler with backoff + body buffering |
| `internal/middleware/retry/retry_test.go` | Create | 15 test cases + 2 integration tests |
| `cmd/gateway/main.go` | Modify | Wire per-route CB + retry |

---

## 2. Circuit Breaker (`circuitbreaker.go`)

### State Machine

```
Closed ──(failureCount >= threshold)──→ Open
Open ──(timeout elapsed)──→ HalfOpen
HalfOpen ──(successCount >= threshold)──→ Closed
HalfOpen ──(any failure)──→ Open
```

### Types

```go
type State int
const (
    StateClosed   State = iota
    StateOpen
    StateHalfOpen
)

var ErrCircuitOpen = errors.New("circuit breaker is open")
```

### Constructor

```go
func New(cfg *config.CircuitBreakerConfig, routeName string, opts ...Option) *CircuitBreaker
```

Options: `WithOnStateChange(fn)` for Prometheus, `WithClock(fn)` for testing.

### Execute Method

```go
func (cb *CircuitBreaker) Execute(fn func() error) error
```

1. Lock, check state
2. Open + timeout elapsed → HalfOpen
3. Open + timeout NOT elapsed → return `ErrCircuitOpen`
4. **Unlock**, execute `fn()` (**outside lock** — critical for concurrency)
5. Lock, record result:
   - Failure: increment failureCount, reset successCount. HalfOpen → Open immediately. Closed → Open if threshold reached.
   - Success: increment successCount, reset failureCount. HalfOpen → Closed if threshold reached.

### Prometheus Integration

`onStateChange` callback (set via option) updates `gateway_circuit_breaker_state` gauge. Callback lives in main.go, not in CB package.

---

## 3. Retry (`retry.go`)

### Public API

```go
func Retry(cfg *config.RetryConfig, cb *CircuitBreaker, proxy http.Handler) http.Handler
func WithCircuitBreaker(cb *CircuitBreaker, proxy http.Handler) http.Handler
```

### Request Body Buffering

Read entire body into `[]byte` before retry loop. Create fresh `io.NopCloser(bytes.NewReader(bodyBytes))` for each attempt.

### Retry Loop

```
for attempt := 0; attempt <= maxRetries; attempt++:
  1. If attempt > 0: sleep with backoff + jitter
     delay = initialDelay * multiplier^(attempt-1)
     jitter = 0.5 + rand.Float64()  // [0.5, 1.5)
     sleep(delay * jitter)
  2. Check context cancellation
  3. Clone request, reset body
  4. Capture response with httptest.NewRecorder()
  5. Execute through CB (or directly if cb==nil)
  6. Success → copy response to real writer, return
  7. ErrCircuitOpen → break immediately
  8. Other error → continue
All exhausted → 502
Circuit open → 503
```

### Context Cancellation

Check `r.Context().Done()` before sleeping:
```go
select {
case <-time.After(sleepDuration):
case <-r.Context().Done():
    writeJSON(w, 499, "client_closed", "client disconnected during retry")
    return
}
```

### Wiring Scenarios in main.go

```go
if route.CircuitBreaker != nil {
    cb := circuitbreaker.New(route.CircuitBreaker, route.Name, ...)
    if route.Retry != nil {
        handler = retry.Retry(route.Retry, cb, handler)
    } else {
        handler = retry.WithCircuitBreaker(cb, handler)
    }
} else if route.Retry != nil {
    handler = retry.Retry(route.Retry, nil, handler)
}
```

---

## 4. Error Responses

| Condition | Status | Body |
|---|---|---|
| All retries exhausted | 502 | `{"error": "bad_gateway", "message": "upstream failed after N attempts: <err>"}` |
| Circuit breaker open | 503 | `{"error": "service_unavailable", "message": "circuit breaker is open"}` |

---

## 5. Test Plan

### `circuitbreaker_test.go`

| Test | Description |
|---|---|
| `TestClosed_RequestsPassThrough` | fn called, no error |
| `TestClosed_ToOpen_AfterNFailures` | State becomes Open after threshold |
| `TestOpen_ImmediateReject` | Returns ErrCircuitOpen, fn NOT called |
| `TestOpen_ToHalfOpen_AfterTimeout` | Clock advanced → HalfOpen, fn called |
| `TestHalfOpen_ToClosed_AfterNSuccesses` | State becomes Closed |
| `TestHalfOpen_ToOpen_OnFailure` | Single failure → Open |
| `TestConcurrentAccess` | 100 goroutines, no races |
| `TestOnStateChangeCallback` | Callback with correct from/to |
| `TestStateString` | Returns "closed", "open", "half_open" |
| `TestCountersResetOnSuccess` | Failures below threshold + success → reset |
| `TestCountersResetOnFailure` | Successes in HalfOpen + failure → reset |

### `retry_test.go`

| Test | Description |
|---|---|
| `TestNoRetries_SingleAttempt` | MaxRetries=0, upstream called once |
| `TestRetryOnRetryableStatus` | 503 three times → upstream called 3 times |
| `TestSucceedsOnSecondAttempt` | 503 then 200 → returns 200 |
| `TestAllRetriesExhausted_Returns502` | Always fails → 502 |
| `TestCircuitOpen_NoRetry_Immediate503` | CB open → 503, upstream NOT called |
| `TestCircuitOpens_MidRetry` | CB threshold < max retries → 503 early |
| `TestBackoffDelaysIncrease` | Captured sleep durations increase exponentially |
| `TestJitterRange` | All delays within [0.5x, 1.5x) |
| `TestRequestBodyReReadable` | POST body identical on retry |
| `TestNonRetryableStatus_NotRetried` | 400 → not retried, passed through |
| `TestNoCircuitBreaker_RetryOnly` | cb=nil, retry works |
| `TestCBOnly_NoRetry` | WithCircuitBreaker, 500s trip CB |
| `TestResponseHeadersCopied` | Custom headers preserved |
| `TestEmptyBody` | GET with no body retries OK |
| `TestLargeBody` | 1MB body correctly re-sent |
| **Integration:** `TestIntegration_FailThenRecover` | 3 failures then success |
| **Integration:** `TestIntegration_CircuitTripsAndRecovers` | Trip → wait → half-open → close |

---

## 6. Implementation Sequence

```
Step 1: circuitbreaker/circuitbreaker.go
Step 2: circuitbreaker/circuitbreaker_test.go
Step 3: retry/retry.go
Step 4: retry/retry_test.go
Step 5: cmd/gateway/main.go modifications
```

---

## 7. Key Design Decisions

1. **fn() executes outside the lock** — avoids holding mutex during slow upstream calls
2. **HalfOpen allows multiple concurrent requests** — optimistic approach, matches spec
3. **Body buffered in memory** — acceptable for JSON APIs; same pattern as Phase 7 transforms
4. **httptest.NewRecorder for response capture** — works for JSON APIs but not streaming
5. **Retry without CB supported** — `cb=nil` handled gracefully
6. **Context cancellation during retry** — prevents wasting resources on disconnected clients

---

## 8. Acceptance Criteria

- [ ] State machine transitions correct for all paths
- [ ] Exponential backoff with jitter in [0.5x, 1.5x) range
- [ ] Request bodies replayed correctly across retries
- [ ] Circuit open → 503, retries exhausted → 502
- [ ] Client cancellation handled gracefully
- [ ] No external dependencies
- [ ] Routes without retry/CB have zero overhead
- [ ] Thread safety verified with `-race`
