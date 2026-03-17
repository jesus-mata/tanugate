package pipeline

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/circuitbreaker"
	"github.com/jesus-mata/tanugate/internal/middleware/retry"
	"github.com/jesus-mata/tanugate/internal/middleware/transform"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/jesus-mata/tanugate/internal/proxy"
	"github.com/jesus-mata/tanugate/internal/router"
)

// BuildHandler creates the full handler pipeline from the given configuration,
// shared dependencies, and middleware registry. It returns the root handler, a
// cleanup function that closes idle connections on the shared transport, and an
// error if handler construction fails (e.g., from invalid regex patterns or
// middleware build errors). The caller must invoke the cleanup function when the
// handler is no longer in use (e.g., after a config reload swap or on shutdown).
func BuildHandler(
	cfg *config.GatewayConfig,
	deps *FactoryDeps,
	registry *Registry,
) (h http.Handler, cleanup func(), err error) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			err = fmt.Errorf("panic building handler: %v\n\n%s", r, buf[:n])
		}
	}()

	// Create a shared transport for all proxy handlers. This avoids creating
	// a separate connection pool per route, reducing file descriptors and
	// enabling connection reuse across routes targeting the same upstream.
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	cleanup = func() { transport.CloseIdleConnections() }

	// 1. Resolve global middlewares once.
	globalResolved, err := config.ResolveMiddlewares(cfg.Middlewares, cfg.MiddlewareDefinitions)
	if err != nil {
		return nil, cleanup, fmt.Errorf("resolving global middlewares: %w", err)
	}

	// 2. Build per-route handlers.
	handlers := make(map[string]http.Handler, len(cfg.Routes))
	for i := range cfg.Routes {
		route := &cfg.Routes[i]

		// a. Create the proxy handler (innermost layer).
		rh := proxy.NewProxyHandler(route, transport)

		// b. Wrap with circuit_breaker + retry (proxy resilience behavior).
		rh = wrapResilience(route, rh, deps.Metrics)

		// c. Resolve per-route middlewares.
		routeResolved, err := config.ResolveMiddlewares(route.Middlewares, cfg.MiddlewareDefinitions)
		if err != nil {
			return nil, cleanup, fmt.Errorf("route %q: resolving middlewares: %w", route.Name, err)
		}

		// d. Filter globals: remove entries whose Name is in route.SkipMiddlewares.
		effectiveGlobals := filterSkipped(globalResolved, route.SkipMiddlewares)

		// e. Merge: effectiveGlobals + routeResolved = allMiddlewares.
		allMiddlewares := make([]config.ResolvedMiddleware, 0, len(effectiveGlobals)+len(routeResolved))
		allMiddlewares = append(allMiddlewares, effectiveGlobals...)
		allMiddlewares = append(allMiddlewares, routeResolved...)

		// f. Extract transform entries and apply them around the proxy+resilience handler.
		//    Transforms bypass the registry — they must wrap the proxy+resilience handler
		//    directly (innermost) to modify upstream request/response bodies.
		var transforms []config.ResolvedMiddleware
		var chainMiddlewares []config.ResolvedMiddleware
		for _, mw := range allMiddlewares {
			if mw.Type == "transform" {
				transforms = append(transforms, mw)
			} else {
				chainMiddlewares = append(chainMiddlewares, mw)
			}
		}

		// Apply transforms in reverse so config order = execution order (first listed = outermost).
		for j := len(transforms) - 1; j >= 0; j-- {
			t := transforms[j]
			tCfg, ok := t.Config.(*config.TransformConfig)
			if !ok {
				return nil, cleanup, fmt.Errorf("route %q: transform middleware %q: expected *config.TransformConfig, got %T", route.Name, t.Name, t.Config)
			}
			rh = transform.RequestTransform(tCfg.Request, tCfg.MaxBodySize)(
				transform.ResponseTransform(tCfg.Response, tCfg.MaxBodySize)(rh))
		}

		// g. Build remaining middleware chain (non-transform) and apply in reverse order.
		//    The first middleware in the list is outermost (executed first on request).
		for j := len(chainMiddlewares) - 1; j >= 0; j-- {
			mw, err := registry.Build(
				chainMiddlewares[j].Type,
				deps,
				route.Name,
				chainMiddlewares[j].Name,
				chainMiddlewares[j].Config,
			)
			if err != nil {
				return nil, cleanup, fmt.Errorf("route %q: building middleware %q (type %q): %w",
					route.Name, chainMiddlewares[j].Name, chainMiddlewares[j].Type, err)
			}
			rh = mw(rh)
		}

		handlers[route.Name] = rh
	}

	// 3. Create the router.
	r := router.New(cfg.Routes, handlers)

	// 4. Build global chain (recovery, requestID, logging, metrics, tracing).
	//    Tracing is outermost so it covers the entire request lifecycle.
	globalMiddlewares := []middleware.Middleware{
		middleware.Recovery(),
		middleware.RequestID(),
		middleware.Logging(deps.Logger),
		deps.Metrics.Middleware(),
	}
	if deps.TracerProvider != nil {
		globalMiddlewares = append([]middleware.Middleware{
			middleware.Tracing(deps.TracerProvider),
		}, globalMiddlewares...)
	}
	globalChain := middleware.Chain(globalMiddlewares...)

	return globalChain(r), cleanup, nil
}

// wrapResilience wraps a handler with circuit breaker and/or retry logic
// based on the route configuration. This mirrors the original buildHandler
// logic for these proxy-level behaviors.
func wrapResilience(route *config.RouteConfig, handler http.Handler, metrics *observability.MetricsCollector) http.Handler {
	if route.CircuitBreaker != nil {
		cb := circuitbreaker.New(route.CircuitBreaker, route.Name,
			circuitbreaker.WithOnStateChange(func(routeName string, from, to circuitbreaker.State) {
				slog.Info("circuit breaker state change", "route", routeName, "from", from, "to", to)
				metrics.CircuitBreakerState.WithLabelValues(routeName, to.String()).Set(1)
				metrics.CircuitBreakerState.WithLabelValues(routeName, from.String()).Set(0)
			}),
		)
		if route.Retry != nil {
			handler = retry.Retry(route.Retry, cb, handler)
		} else {
			handler = retry.WithCircuitBreaker(cb, handler)
		}
	} else if route.Retry != nil {
		handler = retry.Retry(route.Retry, nil, handler)
	}
	return handler
}

// filterSkipped returns a copy of resolved with entries removed whose Name
// appears in skipNames.
func filterSkipped(resolved []config.ResolvedMiddleware, skipNames []string) []config.ResolvedMiddleware {
	if len(skipNames) == 0 {
		return resolved
	}
	skipSet := make(map[string]bool, len(skipNames))
	for _, name := range skipNames {
		skipSet[name] = true
	}
	filtered := make([]config.ResolvedMiddleware, 0, len(resolved))
	for _, mw := range resolved {
		if !skipSet[mw.Name] {
			filtered = append(filtered, mw)
		}
	}
	return filtered
}
