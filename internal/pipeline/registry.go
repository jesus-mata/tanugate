package pipeline

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/jesus-mata/tanugate/internal/middleware"
	"github.com/jesus-mata/tanugate/internal/middleware/auth"
	"github.com/jesus-mata/tanugate/internal/middleware/ratelimit"

	"github.com/jesus-mata/tanugate/internal/observability"
	"go.opentelemetry.io/otel/trace"
)

// FactoryDeps holds shared resources that middleware factories need.
type FactoryDeps struct {
	Limiter        ratelimit.Limiter
	Authenticators map[string]auth.Authenticator
	Metrics        *observability.MetricsCollector
	TrustedProxies []*net.IPNet
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
}

// Factory creates a Middleware from a resolved config.
type Factory func(deps *FactoryDeps, routeName, mwName string, cfg any) (middleware.Middleware, error)

// Registry maps middleware type names to factory functions.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry creates an empty middleware factory registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register adds a factory for the given middleware type name.
func (r *Registry) Register(typeName string, f Factory) {
	r.factories[typeName] = f
}

// Build constructs a middleware instance using the registered factory for typeName.
func (r *Registry) Build(typeName string, deps *FactoryDeps, routeName, mwName string, cfg any) (middleware.Middleware, error) {
	f, ok := r.factories[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown middleware type: %q", typeName)
	}
	return f(deps, routeName, mwName, cfg)
}

// DefaultRegistry creates a registry with all built-in middleware types.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register("cors", corsFactory)
	r.Register("rate_limit", rateLimitFactory)
	r.Register("auth", authFactory)

	return r
}

func corsFactory(_ *FactoryDeps, _, _ string, cfg any) (middleware.Middleware, error) {
	corsCfg, ok := cfg.(*config.CORSConfig)
	if !ok {
		return nil, fmt.Errorf("cors factory: expected *config.CORSConfig, got %T", cfg)
	}
	return middleware.CORSMiddleware(*corsCfg), nil
}

func rateLimitFactory(deps *FactoryDeps, routeName, mwName string, cfg any) (middleware.Middleware, error) {
	rlCfg, ok := cfg.(*config.RateLimitConfig)
	if !ok {
		return nil, fmt.Errorf("rate_limit factory: expected *config.RateLimitConfig, got %T", cfg)
	}
	if rlCfg == nil {
		return nil, fmt.Errorf("rate_limit factory: config is nil")
	}
	return ratelimit.NewRateLimitMiddleware(rlCfg, routeName, mwName, deps.Limiter, deps.Metrics, deps.TrustedProxies), nil
}

func authFactory(deps *FactoryDeps, _, _ string, cfg any) (middleware.Middleware, error) {
	authCfg, ok := cfg.(*config.AuthMiddlewareConfig)
	if !ok {
		return nil, fmt.Errorf("auth factory: expected *config.AuthMiddlewareConfig, got %T", cfg)
	}
	return auth.NewAuthMiddleware(deps.Logger, deps.Authenticators, authCfg.Providers), nil
}

