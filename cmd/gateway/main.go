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

	slog.Info("Starting API Gateway", "port", cfg.Server.Port)

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

	handlers := make(map[string]http.Handler, len(cfg.Routes))
	for i := range cfg.Routes {
		handlers[cfg.Routes[i].Name] = proxy.NewProxyHandler(&cfg.Routes[i])
	}

	r := router.New(cfg.Routes, handlers)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	registry.MustRegister(collectors.NewGoCollector())
	metrics := observability.NewMetricsCollector(registry)

	globalChain := middleware.Chain(
		middleware.Recovery(),
		middleware.RequestID(),
		middleware.Logging(logger),
		metrics.Middleware(),
		auth.Middleware(authenticators),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", observability.HealthHandler(cfg, nil))
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

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
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
