# Phase 3: CORS — Implementation Plan

## Overview

Phase 3 adds CORS support as a global middleware with per-route override capability. It handles OPTIONS preflight requests (returning 204 without forwarding upstream) and injects CORS headers on regular requests.

**Depends on:** Phase 1 (middleware chain, config structs, router context)
**Can run in parallel with:** Phases 4, 5, 6, 7

---

## 1. Files to Create/Modify

| File | Action | Purpose |
|---|---|---|
| `internal/middleware/cors.go` | Create | CORS middleware + per-route override |
| `internal/middleware/cors_test.go` | Create | 16 test cases |
| `cmd/gateway/main.go` | Modify | Add CORS to global chain, wire CORSOverride per-route |

**No external dependencies** — stdlib only.

---

## 2. Design Decisions

### Override Semantics
When a route has a non-nil `CORS *CORSConfig`, it **completely replaces** the global config (not field-level merge). This avoids ambiguity around zero-values vs omitted values in Go structs.

### Architecture
- **Global CORS middleware** — handles OPTIONS preflights with global config; injects headers on regular requests
- **Per-route CORSOverride** — thin middleware added to per-route chain when `routeCfg.CORS != nil`; replaces global CORS headers

### Preflight + Per-Route Override
CORS is positioned before the Router (position 5), so `MatchedRoute` is not in context during preflights. Preflights use the global config only. Per-route overrides apply to regular requests via the per-route chain.

---

## 3. Public API

```go
// Global CORS middleware (position 5 in chain)
func CORS(globalCfg config.CORSConfig) Middleware

// Per-route override (added to per-route chain when route.CORS != nil)
func CORSOverride(routeCfg config.CORSConfig) Middleware
```

---

## 4. Implementation Details

### 4.1 Internal Helpers

```go
func isWildcard(origins []string) bool
func isOriginAllowed(origin string, allowedOrigins []string) bool
func resolveAllowOrigin(origin string, cfg config.CORSConfig) string
func isPreflight(r *http.Request) bool
func handlePreflight(w http.ResponseWriter, origin string, cfg config.CORSConfig)
func injectCORSHeaders(w http.ResponseWriter, origin string, cfg config.CORSConfig)
```

### 4.2 Preflight Detection

A request is a CORS preflight only if ALL three conditions hold:
1. Method is `OPTIONS`
2. `Origin` header is present
3. `Access-Control-Request-Method` header is present

A simple OPTIONS request without CORS headers is forwarded to next handler.

### 4.3 Preflight Response

If origin is allowed:
- `Access-Control-Allow-Origin: <origin or *>`
- `Access-Control-Allow-Methods: <comma-joined>`
- `Access-Control-Allow-Headers: <comma-joined>`
- `Access-Control-Max-Age: <max_age>` (if > 0)
- `Access-Control-Allow-Credentials: true` (if configured)
- `Vary: Origin`
- **204 No Content** — do NOT forward to next handler

### 4.4 Regular Request Headers

- `Access-Control-Allow-Origin: <origin>`
- `Access-Control-Expose-Headers: <comma-joined>` (NOT on preflights)
- `Access-Control-Allow-Credentials: true` (if configured)
- `Vary: Origin`

### 4.5 Wildcard + Credentials Edge Case

`allowed_origins: ["*"]` with `allow_credentials: true` is invalid per CORS spec. Log warning at construction time and use the requesting origin instead of `*`.

### 4.6 Response Writer Wrapper

```go
type corsResponseWriter struct {
    http.ResponseWriter
    origin      string
    cfg         config.CORSConfig
    headersSent bool
}
```

Intercepts `WriteHeader()` to inject CORS headers before status is sent. Implements `Flush()` and `Unwrap()` for compatibility.

### 4.7 No-Origin Bypass

If `Origin` header is absent (same-origin or non-browser request), skip all CORS processing — no headers, no `Vary`.

---

## 5. Chain Position

```
Recovery → RequestID → Logging → Metrics → CORS → Router
```

---

## 6. Test Plan

| # | Test | Description |
|---|---|---|
| 1 | `TestPreflight_AllowedOrigin_Returns204` | 204, correct headers, next NOT called |
| 2 | `TestPreflight_MaxAge` | `Access-Control-Max-Age` header present |
| 3 | `TestPreflight_Credentials` | `Access-Control-Allow-Credentials: true` |
| 4 | `TestRegularRequest_AllowedOrigin` | 200 from handler, CORS headers present |
| 5 | `TestRegularRequest_DisallowedOrigin` | No CORS headers, `Vary: Origin` still set |
| 6 | `TestPreflight_DisallowedOrigin` | 204, no CORS headers, `Vary: Origin` |
| 7 | `TestWildcardOrigin` | `Access-Control-Allow-Origin: *` |
| 8 | `TestWildcardOrigin_WithCredentials` | Uses requesting origin, logs warning |
| 9 | `TestNoOriginHeader` | No CORS headers at all, next called |
| 10 | `TestVaryOrigin_AlwaysSet` | Present on all requests with Origin |
| 11 | `TestMultipleAllowedOrigins` | Only matching origin returned |
| 12 | `TestPerRouteCORSOverride` | Route-specific config used |
| 13 | `TestPerRouteCORSOverride_CompleteReplacement` | Global fields NOT merged |
| 14 | `TestPreflight_NotActualPreflight` | OPTIONS without ACAM header forwarded |
| 15 | `TestResponseWriterFlusher` | Flush delegates correctly |
| 16 | `TestCORSHeaders_NotDuplicated` | `Set()` overwrites, doesn't append |

---

## 7. Acceptance Criteria

- [ ] Preflight OPTIONS returns 204 with correct headers, never forwarded upstream
- [ ] Regular requests from allowed origins get CORS response headers
- [ ] Disallowed origins get no CORS headers except `Vary: Origin`
- [ ] Wildcard + credentials logs warning and uses requesting origin
- [ ] Per-route override completely replaces global config
- [ ] `Vary: Origin` set on every response with Origin header
- [ ] `corsResponseWriter` implements `http.Flusher` and `Unwrap()`
- [ ] No data races under `go test -race`
