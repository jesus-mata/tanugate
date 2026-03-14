# Hot Reload

Tanugate supports zero-downtime configuration reload via the `SIGHUP` signal. When SIGHUP is received, the gateway re-reads the configuration file, validates it, builds a new handler pipeline, and atomically swaps it in — all without dropping existing connections.

## Reloadable vs Non-Reloadable

| Reloadable (applied on SIGHUP) | Non-reloadable (logged as warning, ignored) |
|---|---|
| Routes (add/remove/modify) | Server host/port/timeouts |
| Rate limit settings (per-route) | Rate limit backend type |
| CORS configuration (global + per-route) | Redis connection settings |
| Transform rules | Auth provider definitions |
| Circuit breaker thresholds | Logging level |
| Retry settings | |
| Auth provider assignment to routes | |

When non-reloadable fields change, a warning is logged for each one and only reloadable parts are applied.

## How to Trigger a Reload

### Direct signal

```bash
kill -HUP $(pgrep gateway)
```

### Docker

```bash
docker kill --signal=HUP <container_id>
```

### Docker Compose / Swarm

```bash
docker compose kill -s HUP gateway
```

### Kubernetes

```bash
kubectl exec <pod-name> -- kill -HUP 1
```

## Reload Behavior

1. **Load** — The configuration file is re-read from disk (same path as the original `-config` flag).
2. **Validate** — Full validation runs including regex compilation, auth provider references, etc.
3. **Check non-reloadable changes** — Each non-reloadable field that differs is logged as a warning.
4. **Build** — A new handler pipeline (per-route handlers + router + global middleware chain) is constructed.
5. **Swap** — The new handler is atomically swapped in via `sync/atomic.Pointer`.
6. **Log** — A summary of what changed (routes added/removed/modified, CORS changes) is logged.

### What Happens on Invalid Config

If the new configuration fails validation (bad YAML, invalid regex, missing auth provider references), the reload is aborted entirely. The old handler continues serving all requests, and an error is logged:

```
ERROR config reload failed: invalid configuration, keeping current config
```

### What Happens on Build Failure

If handler construction panics (e.g., from unexpected runtime errors), the panic is recovered, an error is logged, and the old handler continues serving.

## In-Flight Request Safety

The atomic swap pattern ensures:

- **In-flight requests** complete with the old handler (they hold a reference to it).
- **New requests** after the swap use the new handler.
- **No locking** on the hot path — just one atomic pointer load per request.

## Rate Limit State Preservation

The rate limiter instance is **reused across reloads**. Token bucket state (or Redis state) persists:

- Existing rate limit counters survive a reload.
- If a route is removed, stale buckets are cleaned up by the memory limiter's background goroutine (evicts idle entries after ~5 minutes).
- If a route's rate limit settings change (e.g., `requests_per_window` increases), new requests use the new limits while existing bucket state naturally expires.

## Circuit Breaker State

Circuit breakers are **recreated on reload** (state resets). This is intentional — upstream configuration may have changed, and fresh state matches the new thresholds.

## Example Workflow

1. Start the gateway:

```bash
./bin/gateway -config config/gateway.yaml
```

2. Edit the config to change a route's rate limit:

```yaml
routes:
  - name: "api"
    rate_limit:
      requests_per_window: 200  # was 100
      window: 1m
```

3. Send SIGHUP:

```bash
kill -HUP $(pgrep gateway)
```

4. Check the logs:

```
INFO Received SIGHUP, reloading configuration... path=config/gateway.yaml
INFO Configuration reloaded successfully changes=["route \"api\": modified"]
```

5. Verify the new rate limit is in effect by checking `X-RateLimit-Limit` response headers.
