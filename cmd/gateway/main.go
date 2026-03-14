package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/middleware/circuitbreaker"
	"github.com/jesus-mata/tanugate/internal/middleware/ratelimit"
	"github.com/jesus-mata/tanugate/internal/middleware/retry"
	"github.com/jesus-mata/tanugate/internal/middleware/transform"
	"github.com/jesus-mata/tanugate/internal/observability"
	"github.com/jesus-mata/tanugate/internal/proxy"
	"github.com/jesus-mata/tanugate/internal/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// handlerRef wraps an http.Handler for use with atomic.Pointer.
type handlerRef struct{ h http.Handler }

func main() {
	configPath := flag.String("config", "config/gateway.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logLevel := parseLogLevel(cfg.Logging.Level)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	trustedProxies, err := ratelimit.ParseTrustedProxies(cfg.Server.TrustedProxies)
	if err != nil {
		slog.Error("failed to parse trusted proxies", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting API Gateway", "port", cfg.Server.Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build rate limiter backend.
	var limiter ratelimit.Limiter
	var healthChecker observability.HealthChecker
	switch cfg.RateLimit.Backend {
	case "redis":
		if cfg.RateLimit.Redis == nil {
			slog.Error("redis rate limit backend requires redis config")
			os.Exit(1)
		}
		rl := ratelimit.NewRedisLimiter(cfg.RateLimit.Redis)
		if err := rl.HealthCheck(ctx); err != nil {
			slog.Error("failed to connect to Redis for rate limiting",
				"addr", cfg.RateLimit.Redis.Addr,
				"error", err,
			)
			os.Exit(1)
		}
		limiter = rl
		healthChecker = rl
		slog.Info("Rate limit backend: redis", "addr", cfg.RateLimit.Redis.Addr)
	default:
		limiter = ratelimit.NewMemoryLimiter(ctx)
		slog.Info("Rate limit backend: memory")
	}

	// Build auth providers.
	authenticators := make(map[string]auth.Authenticator, len(cfg.AuthProviders))
	for name, provider := range cfg.AuthProviders {
		a, err := auth.NewAuthenticator(provider)
		if err != nil {
			slog.Error("failed to create auth provider", "name", name, "error", err)
			os.Exit(1)
		}
		authenticators[name] = a
		slog.Info("Registered auth provider", "name", name, "type", provider.Type)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	registry.MustRegister(collectors.NewGoCollector())
	metrics := observability.NewMetricsCollector(registry)

	handler, err := buildHandler(cfg, limiter, authenticators, metrics, trustedProxies, logger)
	if err != nil {
		slog.Error("failed to build handler", "error", err)
		os.Exit(1)
	}

	var current atomic.Pointer[handlerRef]
	current.Store(&handlerRef{h: handler})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", observability.HealthHandler(cfg, healthChecker))
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current.Load().h.ServeHTTP(w, r)
	}))

	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

loop:
	for sig := range sigCh {
		switch sig {
		case syscall.SIGHUP:
			handleReload(*configPath, cfg, &current, limiter, authenticators, metrics, trustedProxies, logger)
		default:
			break loop
		}
	}

	slog.Info("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()
	cancel() // Stop background goroutines (memory limiter cleanup).

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	// Stop auth providers that have background goroutines.
	for name, authn := range authenticators {
		if stopper, ok := authn.(interface{ Stop() }); ok {
			stopper.Stop()
			slog.Info("Stopped auth provider", "name", name)
		}
	}

	// Close Redis client if applicable.
	if rl, ok := limiter.(*ratelimit.RedisLimiter); ok {
		if err := rl.Close(); err != nil {
			slog.Error("failed to close redis client", "error", err)
		}
	}

	slog.Info("Server stopped")
}

// buildHandler creates the full handler pipeline from the given configuration
// and shared resources. It returns an error if handler construction fails
// (e.g., from invalid regex patterns).
func buildHandler(
	cfg *config.GatewayConfig,
	limiter ratelimit.Limiter,
	authenticators map[string]auth.Authenticator,
	metrics *observability.MetricsCollector,
	trustedProxies []*net.IPNet,
	logger *slog.Logger,
) (h http.Handler, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic building handler: %v", r)
		}
	}()

	handlers := make(map[string]http.Handler, len(cfg.Routes))
	for i := range cfg.Routes {
		rh := proxy.NewProxyHandler(&cfg.Routes[i])

		route := &cfg.Routes[i]
		if route.CircuitBreaker != nil {
			cb := circuitbreaker.New(route.CircuitBreaker, route.Name,
				circuitbreaker.WithOnStateChange(func(routeName string, from, to circuitbreaker.State) {
					slog.Info("circuit breaker state change", "route", routeName, "from", from, "to", to)
					metrics.CircuitBreakerState.WithLabelValues(routeName, to.String()).Set(1)
					metrics.CircuitBreakerState.WithLabelValues(routeName, from.String()).Set(0)
				}),
			)
			if route.Retry != nil {
				rh = retry.Retry(route.Retry, cb, rh)
			} else {
				rh = retry.WithCircuitBreaker(cb, rh)
			}
		} else if route.Retry != nil {
			rh = retry.Retry(route.Retry, nil, rh)
		}

		if route.Transform != nil {
			rh = transform.RequestTransform(route.Transform.Request, route.Transform.MaxBodySize)(
				transform.ResponseTransform(route.Transform.Response, route.Transform.MaxBodySize)(rh))
		}

		if cfg.Routes[i].CORS != nil {
			rh = middleware.CORSOverride(*cfg.Routes[i].CORS)(rh)
		}

		rh = auth.Middleware(logger, authenticators)(rh)
		rh = ratelimit.RateLimit(limiter, metrics, trustedProxies)(rh)

		handlers[cfg.Routes[i].Name] = rh
	}

	r := router.New(cfg.Routes, handlers)

	globalChain := middleware.Chain(
		middleware.Recovery(),
		middleware.RequestID(),
		middleware.Logging(logger),
		metrics.Middleware(),
		middleware.CORS(cfg.CORS),
	)

	return globalChain(r), nil
}

// handleReload loads the configuration from disk, validates it, builds a new
// handler pipeline, and atomically swaps it in. If any step fails, the old
// handler continues serving and an error is logged.
func handleReload(
	configPath string,
	currentCfg *config.GatewayConfig,
	current *atomic.Pointer[handlerRef],
	limiter ratelimit.Limiter,
	authenticators map[string]auth.Authenticator,
	metrics *observability.MetricsCollector,
	trustedProxies []*net.IPNet,
	logger *slog.Logger,
) {
	slog.Info("Received SIGHUP, reloading configuration...", "path", configPath)

	newCfg, err := config.LoadConfig(configPath)
	if err != nil {
		slog.Error("config reload failed: invalid configuration, keeping current config", "error", err)
		return
	}

	// Warn about non-reloadable changes.
	for _, w := range config.NonReloadableChanges(currentCfg, newCfg) {
		slog.Warn("non-reloadable change detected (ignored)", "detail", w)
	}

	newHandler, err := buildHandler(newCfg, limiter, authenticators, metrics, trustedProxies, logger)
	if err != nil {
		slog.Error("config reload failed: could not build handler, keeping current config", "error", err)
		return
	}

	current.Store(&handlerRef{h: newHandler})

	// Log what changed.
	changes := config.DiffSummary(currentCfg, newCfg)
	if len(changes) == 0 {
		slog.Info("Configuration reloaded (no reloadable changes detected)")
	} else {
		slog.Info("Configuration reloaded successfully", "changes", changes)
	}

	// Update the current config reference for future reloads.
	// Copy the reloadable fields into the current config.
	currentCfg.Routes = newCfg.Routes
	currentCfg.CORS = newCfg.CORS
}

func parseLogLevel(level string) slog.Level {
	levels := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}

	if lvl, ok := levels[level]; ok {
		return lvl
	}
	return slog.LevelInfo
}
