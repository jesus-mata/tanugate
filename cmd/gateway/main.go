package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/NextSolutionCUU/api-gateway/internal/config"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/auth"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/circuitbreaker"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/ratelimit"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/retry"
	"github.com/NextSolutionCUU/api-gateway/internal/middleware/transform"
	"github.com/NextSolutionCUU/api-gateway/internal/observability"
	"github.com/NextSolutionCUU/api-gateway/internal/proxy"
	"github.com/NextSolutionCUU/api-gateway/internal/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

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

	handlers := make(map[string]http.Handler, len(cfg.Routes))
	for i := range cfg.Routes {
		h := proxy.NewProxyHandler(&cfg.Routes[i])

		// Wrap with circuit breaker and/or retry.
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
				h = retry.Retry(route.Retry, cb, h)
			} else {
				h = retry.WithCircuitBreaker(cb, h)
			}
		} else if route.Retry != nil {
			h = retry.Retry(route.Retry, nil, h)
		}

		// Wrap with request/response transforms.
		if route.Transform != nil {
			h = transform.RequestTransform(route.Transform.Request, route.Transform.MaxBodySize)(
				transform.ResponseTransform(route.Transform.Response, route.Transform.MaxBodySize)(h))
		}

		if cfg.Routes[i].CORS != nil {
			h = middleware.CORSOverride(*cfg.Routes[i].CORS)(h)
		}

		// Auth and rate-limit are per-route: the router sets route context
		// before dispatching, so these middleware can read the matched route.
		h = auth.Middleware(authenticators)(h)
		h = ratelimit.RateLimit(limiter, metrics, trustedProxies)(h)

		handlers[cfg.Routes[i].Name] = h
	}

	r := router.New(cfg.Routes, handlers)

	globalChain := middleware.Chain(
		middleware.Recovery(),
		middleware.RequestID(),
		middleware.Logging(logger),
		metrics.Middleware(),
		middleware.CORS(cfg.CORS),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", observability.HealthHandler(cfg, healthChecker))
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.Handle("/", globalChain(r))

	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()
	cancel() // Stop background goroutines (memory limiter cleanup).

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	// Close Redis client if applicable.
	if rl, ok := limiter.(*ratelimit.RedisLimiter); ok {
		if err := rl.Close(); err != nil {
			slog.Error("failed to close redis client", "error", err)
		}
	}

	slog.Info("Server stopped")
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
