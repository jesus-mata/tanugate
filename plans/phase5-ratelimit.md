# Phase 5: Rate Limiting — Implementation Plan

## Overview

Phase 5 introduces per-route rate limiting with two backends: in-memory token bucket and Redis sliding window. The middleware extracts a key from the request (IP, header, or JWT claim), checks the limiter, and returns 429 with appropriate headers when exceeded.

**Depends on:** Phase 1 (middleware chain, config structs, router context)
**Soft dependency on Phase 4:** `key_source: "claim:<name>"` needs AuthResult in context; falls back to IP gracefully
**Can run in parallel with:** Phases 3, 6, 7
**External dependency:** `github.com/redis/go-redis/v9` v9.7+

---

## 1. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `internal/middleware/ratelimit/ratelimit.go` | Create | Limiter interface, middleware, key extraction |
| `internal/middleware/ratelimit/ratelimit_test.go` | Create | 13 test cases |
| `internal/middleware/ratelimit/memory.go` | Create | In-memory token bucket with cleanup goroutine |
| `internal/middleware/ratelimit/memory_test.go` | Create | 9 test cases |
| `internal/middleware/ratelimit/redis.go` | Create | Redis sliding window with Lua script |
| `internal/middleware/ratelimit/redis_test.go` | Create | 6 test cases |
| `cmd/gateway/main.go` | Modify | Create limiter backend, wire into per-route chains |

---

## 2. Limiter Interface

```go
type Limiter interface {
    Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, remaining int, resetAt time.Time, err error)
}
```

- `key` is a composite string: `routeName + ":" + extractedKey`
- `limit` and `window` are per-call (routes have different limits, same Limiter instance)
- Returns four values for header population

---

## 3. Key Extraction

```go
func extractKey(r *http.Request, keySource string) (string, error)
```

| Key Source | Resolution |
|---|---|
| `"ip"` | `X-Forwarded-For` first IP → `RemoteAddr` fallback |
| `"header:<name>"` | Header value → IP fallback if empty |
| `"claim:<name>"` | AuthResult from context → IP fallback if missing |

Fallback to IP with `slog.Warn` when header/claim is unavailable.

---

## 4. Rate Limit Middleware

```go
func RateLimit(limiter Limiter) middleware.Middleware
```

**Position in per-route chain:** 7 (before auth at 8)

**Logic:**
1. Get `MatchedRoute` from context → `route.RateLimit`
2. Skip if `RateLimit == nil`
3. Extract key, compose `routeName + ":" + key`
4. Call `limiter.Allow()`
5. **On error (backend failure):** fail open, log error, allow request
6. Set response headers (always, not just 429):
   - `X-RateLimit-Limit`
   - `X-RateLimit-Remaining`
   - `X-RateLimit-Reset` (Unix timestamp)
7. If not allowed → 429:
   ```json
   {"error": "rate_limit_exceeded", "message": "Rate limit exceeded. Try again in N seconds.", "retry_after": N}
   ```
   Also set `Retry-After` header. Increment `gateway_rate_limit_rejected_total` counter.

---

## 5. In-Memory Token Bucket (`memory.go`)

```go
type MemoryLimiter struct {
    buckets sync.Map // key → *bucket
}

type bucket struct {
    mu         sync.Mutex
    tokens     float64
    maxTokens  float64
    refillRate float64    // tokens per second = limit / window.Seconds()
    lastRefill time.Time
    lastAccess time.Time  // for cleanup eviction
}
```

### Constructor
```go
func NewMemoryLimiter(ctx context.Context) *MemoryLimiter
```
Starts background cleanup goroutine (every 60s, evicts buckets idle > 5min). Accepts context for shutdown.

### Allow()
1. Compute `refillRate = limit / window.Seconds()`
2. `LoadOrStore` bucket in sync.Map
3. Lock bucket, refill tokens based on elapsed time, cap at max
4. Try consuming 1 token
5. Return result

### Thread Safety
- `sync.Map` for map operations (optimized for stable keys)
- Per-bucket `sync.Mutex` (no global contention)

---

## 6. Redis Sliding Window (`redis.go`)

### Lua Script
```lua
local key = KEYS[1]
local window = tonumber(ARGV[1])  -- milliseconds
local limit = tonumber(ARGV[2])
local now = tonumber(ARGV[3])     -- milliseconds

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, now .. '-' .. math.random(1000000))
    redis.call('EXPIRE', key, math.ceil(window / 1000))
    return {1, limit - count - 1, now + window}
else
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local reset = now + window
    if #oldest > 0 then reset = tonumber(oldest[2]) + window end
    return {0, 0, reset}
end
```

### Constructor
```go
func NewRedisLimiter(cfg config.RedisConfig) *RedisLimiter
```
Creates `redis.Client`, pre-registers Lua script via `redis.NewScript()`.

### Health Check
```go
func (r *RedisLimiter) HealthCheck(ctx context.Context) error  // For /health endpoint
func (r *RedisLimiter) Close() error                           // For graceful shutdown
```

---

## 7. Test Plans

### `ratelimit_test.go` (mock limiter)

| Test | Description |
|---|---|
| `TestRateLimit_SkipsWhenNoConfig` | No rate limit config → passthrough |
| `TestRateLimit_AllowedRequest` | 200 + all three headers present |
| `TestRateLimit_RejectedRequest` | 429 + JSON body + Retry-After |
| `TestRateLimit_ResponseHeaders_AlwaysSet` | Headers on 200 too |
| `TestRateLimit_429ResponseFormat` | Verify error/message/retry_after fields |
| `TestKeyExtract_IP_RemoteAddr` | No XFF → RemoteAddr |
| `TestKeyExtract_IP_XForwardedFor` | Multiple IPs → first one |
| `TestKeyExtract_Header` | Header value extracted |
| `TestKeyExtract_Header_Missing` | Falls back to IP |
| `TestKeyExtract_Claim` | JWT claim extracted |
| `TestKeyExtract_Claim_NoAuthContext` | Falls back to IP |
| `TestRateLimit_LimiterError_FailOpen` | Error → request allowed |
| `TestRateLimit_CompositeKey` | Key is `routeName:extractedKey` |

### `memory_test.go`

| Test | Description |
|---|---|
| `TestMemory_AllowUpToLimit` | N requests allowed |
| `TestMemory_RejectAfterLimit` | N+1 rejected |
| `TestMemory_TokenRefill` | Partial window → some tokens refilled |
| `TestMemory_FullRefillAfterWindow` | Full window → full quota |
| `TestMemory_DifferentKeys` | Independent buckets |
| `TestMemory_ConcurrentAccess` | N goroutines, total allowed = limit |
| `TestMemory_CleanupEvictsExpired` | Old bucket removed |
| `TestMemory_CleanupPreservesActive` | Active bucket kept |
| `TestMemory_ContextCancellation` | Cleanup goroutine exits |

### `redis_test.go` (skip if no TEST_REDIS_ADDR)

| Test | Description |
|---|---|
| `TestRedis_AllowUpToLimit` | N requests allowed |
| `TestRedis_RejectAfterLimit` | N+1 rejected |
| `TestRedis_SlidingWindow` | Entries expire mid-window |
| `TestRedis_LuaScript_Atomicity` | Concurrent goroutines, total = limit |
| `TestRedis_KeyExpiry` | Key auto-deleted after window |
| `TestRedis_Ping` | Health check works |

---

## 8. Architecture Decisions

1. **Rate limit before auth (position 7 vs 8):** Prevents unauthenticated flood from consuming auth resources. `claim:` key degrades to IP when auth context missing.
2. **Fail open on backend errors:** Redis outage shouldn't cause gateway outage.
3. **Token bucket (memory) vs sliding window (Redis):** Different algorithms by design — token bucket is smoother, sliding window is more precise.
4. **`sync.Map` + per-bucket mutex:** Scales well under high concurrency, no global lock contention.

---

## 9. Acceptance Criteria

- [ ] Routes without `rate_limit:` pass through with no headers
- [ ] Rate limit enforced within configured window
- [ ] All three `X-RateLimit-*` headers on every response for rate-limited routes
- [ ] 429 response includes JSON body + `Retry-After` header
- [ ] Key extraction works for ip, header, claim sources
- [ ] Memory backend: tokens refill, cleanup works, no races
- [ ] Redis backend: sliding window correct, Lua atomic, keys expire
- [ ] Backend failure → fail open with error log
- [ ] `gateway_rate_limit_rejected_total` counter increments on 429
- [ ] All tests pass with `go test -race`
