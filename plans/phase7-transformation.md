# Phase 7: Request/Response Transformation — Implementation Plan

## Overview

Phase 7 implements request and response transformation middleware. It modifies HTTP headers and JSON body content based on per-route YAML config, supporting dynamic variable interpolation (`${request_id}`, `${client_ip}`, `${latency_ms}`, etc.).

**Depends on:** Phase 1 (middleware chain, config structs, router context, requestid)
**Can run in parallel with:** Phases 3, 4, 5, 6
**No external dependencies** — stdlib only

---

## 1. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `internal/middleware/transform/transform.go` | Create | Request/response transform middleware, header/body transforms, variable resolver |
| `internal/middleware/transform/transform_test.go` | Create | ~50 subtests |
| `cmd/gateway/main.go` | Modify | Wire transforms into per-route chains |

---

## 2. Architecture: Two Separate Middleware

```go
func RequestTransform(cfg *config.DirectionTransform) middleware.Middleware  // Position 9
func ResponseTransform(cfg *config.DirectionTransform) middleware.Middleware // Position 12
```

Request transform modifies the request before proxy. Response transform uses a buffering writer to capture the proxy response, transforms it, then flushes to the real writer.

### Chain Ordering
```
[RateLimit, Auth, ReqTransform, ResTransform, CB+Retry+Proxy]
```
Chain applies as: `RateLimit(Auth(ReqTransform(ResTransform(Proxy))))`

On the way in: RateLimit → Auth → ReqTransform (modifies request) → ResTransform (installs buffer) → Proxy
On the way out: Proxy writes to buffer → ResTransform (transforms, flushes) → rest unwind

---

## 3. Dynamic Variables

| Variable | Resolution |
|---|---|
| `${request_id}` | `middleware.RequestIDFromContext(r.Context())` |
| `${client_ip}` | X-Forwarded-For first IP → RemoteAddr fallback |
| `${timestamp_iso}` | `time.Now().UTC().Format(time.RFC3339)` |
| `${timestamp_unix}` | `strconv.FormatInt(time.Now().Unix(), 10)` |
| `${latency_ms}` | Response transforms only; computed from start time in context |
| `${route_name}` | `router.RouteFromContext(r.Context()).Config.Name` |
| `${method}` | `r.Method` |
| `${path}` | `r.URL.Path` |

Implementation: `regexp.MustCompile(`\$\{([^}]+)\}`)` with `ReplaceAllStringFunc`. Unknown variables left unchanged.

---

## 4. Header Transforms

```go
func transformHeaders(h http.Header, cfg *config.HeaderTransform, r *http.Request, latencyMs *int64)
```

Order of operations:
1. **Remove** — `h.Del(name)` for each
2. **Rename** — copy values to new name, delete old (handles multi-value headers)
3. **Add** — `h.Set(name, resolveVariables(value))` (overwrites if exists)

---

## 5. Body Transforms

```go
func transformBody(body []byte, cfg *config.BodyTransform, r *http.Request, latencyMs *int64) ([]byte, error)
```

**Only applies to `Content-Type: application/json`**. Non-JSON passes through unchanged.

Order of operations:
1. **Strip fields** — `delete(data, field)` for top-level keys
2. **Rename keys** — copy value to new key, delete old
3. **Inject fields** — `data[key] = resolveVariables(value)` (injected values are always strings)

After modification: update `Content-Length` header.

---

## 6. Buffering Response Writer

```go
type bufferingResponseWriter struct {
    headers     http.Header
    body        bytes.Buffer
    statusCode  int
    wroteHeader bool
}
```

Does NOT embed `http.ResponseWriter` — implements interface from scratch to prevent premature writes. Does NOT implement `http.Flusher` (would send data before transforms). Provides `Unwrap()` for compatibility.

---

## 7. Request Transform Middleware

1. Store `time.Now()` in context (for `${latency_ms}`)
2. If cfg is nil → passthrough (still store start time)
3. Apply header transforms to `r.Header`
4. If Content-Type is JSON and body config exists:
   - Read body, transform, replace with new reader
   - Update `Content-Length`
5. Call `next.ServeHTTP(w, r)`

---

## 8. Response Transform Middleware

1. If cfg is nil → passthrough
2. Create `bufferingResponseWriter`
3. Call `next.ServeHTTP(bufWriter, r)` — proxy writes to buffer
4. Compute latency from context start time
5. Apply header transforms to buffered headers
6. If Content-Type is JSON and body config exists: transform body, update Content-Length
7. Remove `Transfer-Encoding: chunked` if present (we write full body)
8. Copy headers to real writer → WriteHeader → Write body

---

## 9. Test Plan (~50 subtests)

### `TestResolveVariables` (~13 subtests)
- Static string unchanged
- Each of 8 variables resolves correctly
- Multiple variables in one string
- Unknown variable left unchanged
- Nil latency → "0"

### `TestTransformHeaders` (~9 subtests)
- Remove, Rename, Add operations
- Dynamic variables in Add values
- Order: remove → rename → add
- Nil config is no-op
- Non-existent header operations are no-ops

### `TestTransformBody` (~11 subtests)
- Strip, rename, inject operations
- Dynamic variables in inject values
- Order: strip → rename → inject
- Nested JSON: only top-level affected
- Non-JSON body unchanged
- Empty body unchanged
- Nil config is no-op
- JSON array body unchanged (not `map[string]any`)

### `TestRequestTransformMiddleware` (~5 subtests)
- Header + body transforms applied
- Content-Length updated
- Non-JSON Content-Type: body unchanged
- Nil config passthrough
- Start time stored in context

### `TestResponseTransformMiddleware` (~6 subtests)
- Header + body transforms applied
- Content-Length updated
- Non-JSON response unchanged
- Status code preserved
- `${latency_ms}` available
- Nil config passthrough

### `TestBufferingResponseWriter` (~5 subtests)
- Header capture, status capture, body buffering
- Multiple Write() calls accumulate
- Implicit 200 status

### `TestIntegration_RequestAndResponseTransform` (~1 test)
- Full chain with echo handler
- Both request and response transforms
- Verify end-to-end field manipulation

---

## 10. Implementation Sequence

```
Step 1: Helper functions (clientIP, isJSON, resolveVariables)
Step 2: transformHeaders, transformBody
Step 3: bufferingResponseWriter
Step 4: RequestTransform, ResponseTransform middleware
Step 5: Tests
Step 6: Wire into main.go
```

---

## 11. Edge Cases

| Edge Case | Handling |
|---|---|
| Non-JSON body | Passes through unchanged |
| Invalid JSON (with JSON Content-Type) | Passes through, debug warning |
| Empty body | Skip body transforms |
| JSON array body | Not `map[string]any`, passes through |
| Large body | Buffered in memory (acceptable for API traffic) |
| Transfer-Encoding: chunked | Removed after buffering, Content-Length set |
| Missing start time in context | Use `time.Now()` (latency ≈ 0) |

---

## 12. Acceptance Criteria

- [ ] Request headers transformed before reaching upstream
- [ ] Response headers transformed before reaching client
- [ ] JSON body fields stripped, renamed, injected correctly
- [ ] Dynamic variables resolve correctly in both headers and body
- [ ] Content-Length updated after all body modifications
- [ ] Non-JSON bodies pass through unchanged
- [ ] Nil configs are zero-overhead passthroughs
- [ ] `${latency_ms}` available in response transforms
- [ ] All tests pass with `go test -race`
