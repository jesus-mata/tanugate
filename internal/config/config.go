package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GatewayConfig is the top-level configuration for the API gateway.
type GatewayConfig struct {
	Server        ServerConfig            `yaml:"server"`
	Logging       LoggingConfig           `yaml:"logging"`
	CORS          CORSConfig              `yaml:"cors"`
	RateLimit     RateLimitGlobalConfig   `yaml:"rate_limit"`
	AuthProviders map[string]AuthProvider `yaml:"auth_providers"`
	Routes        []RouteConfig           `yaml:"routes"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	TrustedProxies  []string      `yaml:"trusted_proxies"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `yaml:"level"`
}

// CORSConfig holds cross-origin resource sharing settings.
type CORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins"`
	AllowedMethods   []string `yaml:"allowed_methods"`
	AllowedHeaders   []string `yaml:"allowed_headers"`
	ExposedHeaders   []string `yaml:"exposed_headers"`
	AllowCredentials bool     `yaml:"allow_credentials"`
	MaxAge           int      `yaml:"max_age"`
}

// RateLimitGlobalConfig holds global rate-limiting settings.
type RateLimitGlobalConfig struct {
	Backend string       `yaml:"backend"`
	Redis   *RedisConfig `yaml:"redis"`
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	Addr         string        `yaml:"addr"`
	Password     string        `yaml:"password"`
	DB           int           `yaml:"db"`
	PoolSize     int           `yaml:"pool_size"`
	MinIdleConns int           `yaml:"min_idle_conns"`
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	MaxRetries   int           `yaml:"max_retries"`
	QueryTimeout time.Duration `yaml:"query_timeout"`
	TLSEnabled   bool          `yaml:"tls_enabled"`
}

// AuthProvider describes a single authentication provider.
type AuthProvider struct {
	Type   string        `yaml:"type"`
	JWT    *JWTConfig    `yaml:"jwt"`
	APIKey *APIKeyConfig `yaml:"api_key"`
	OIDC   *OIDCConfig   `yaml:"oidc"`
}

// JWTConfig holds JWT authentication settings.
type JWTConfig struct {
	Secret        string `yaml:"secret"`
	PublicKeyFile string `yaml:"public_key_file"`
	Algorithm     string `yaml:"algorithm"`
	Issuer        string `yaml:"issuer"`
	Audience      string `yaml:"audience"`
}

// APIKeyConfig holds API-key authentication settings.
type APIKeyConfig struct {
	Header string        `yaml:"header"`
	Keys   []APIKeyEntry `yaml:"keys"`
}

// APIKeyEntry represents a single API key and its human-readable name.
type APIKeyEntry struct {
	Key  string `yaml:"key"`
	Name string `yaml:"name"`
}

// OIDCConfig holds OpenID Connect authentication settings.
type OIDCConfig struct {
	IssuerURL         string   `yaml:"issuer_url"`
	JWKSURL           string   `yaml:"jwks_url"`
	IntrospectionURL  string   `yaml:"introspection_url"`
	ClientID          string   `yaml:"client_id"`
	ClientSecret      string   `yaml:"client_secret"`
	Audience          string   `yaml:"audience"`
	AllowedAlgorithms []string `yaml:"allowed_algorithms"`
	SkipIssuerCheck   bool     `yaml:"skip_issuer_check"`
}

// RouteConfig describes a single route handled by the gateway.
type RouteConfig struct {
	Name           string                `yaml:"name"`
	Match          MatchConfig           `yaml:"match"`
	Upstream       UpstreamConfig        `yaml:"upstream"`
	CORS           *CORSConfig           `yaml:"cors"`
	Auth           *RouteAuthConfig      `yaml:"auth"`
	RateLimit      *RouteLimitConfig     `yaml:"rate_limit"`
	Retry          *RetryConfig          `yaml:"retry"`
	CircuitBreaker *CircuitBreakerConfig `yaml:"circuit_breaker"`
	Transform      *TransformConfig      `yaml:"transform"`
}

// MatchConfig holds request-matching criteria for a route.
type MatchConfig struct {
	PathRegex string   `yaml:"path_regex"`
	Methods   []string `yaml:"methods"`
}

// UpstreamConfig holds upstream target settings for a route.
type UpstreamConfig struct {
	URL         string        `yaml:"url"`
	PathRewrite string        `yaml:"path_rewrite"`
	Timeout     time.Duration `yaml:"timeout"`
}

// RouteAuthConfig selects the authentication providers for a route.
// Providers are tried in order; the first to succeed authenticates the request.
type RouteAuthConfig struct {
	Providers []string `yaml:"providers"`
}

// RouteLimitConfig holds per-route rate-limiting settings.
type RouteLimitConfig struct {
	RequestsPerWindow int           `yaml:"requests_per_window"`
	Window            time.Duration `yaml:"window"`
	KeySource         string        `yaml:"key_source"`
	Algorithm         string        `yaml:"algorithm"`
}

// RetryConfig holds retry behaviour settings for a route.
type RetryConfig struct {
	MaxRetries           int           `yaml:"max_retries"`
	InitialDelay         time.Duration `yaml:"initial_delay"`
	Multiplier           float64       `yaml:"multiplier"`
	RetryableStatusCodes []int         `yaml:"retryable_status_codes"`
}

// CircuitBreakerConfig holds circuit-breaker settings for a route.
type CircuitBreakerConfig struct {
	FailureThreshold int           `yaml:"failure_threshold"`
	SuccessThreshold int           `yaml:"success_threshold"`
	Timeout          time.Duration `yaml:"timeout"`
}

// TransformConfig holds request/response transformation rules for a route.
type TransformConfig struct {
	Request     *DirectionTransform `yaml:"request"`
	Response    *DirectionTransform `yaml:"response"`
	MaxBodySize int64               `yaml:"max_body_size"`
}

// DirectionTransform describes header and body transformations applied in one
// direction (request or response).
type DirectionTransform struct {
	Headers *HeaderTransform `yaml:"headers"`
	Body    *BodyTransform   `yaml:"body"`
}

// HeaderTransform describes modifications to HTTP headers.
type HeaderTransform struct {
	Add    map[string]string `yaml:"add"`
	Remove []string          `yaml:"remove"`
	Rename map[string]string `yaml:"rename"`
}

// BodyTransform describes modifications to an HTTP body.
type BodyTransform struct {
	InjectFields map[string]any    `yaml:"inject_fields"`
	StripFields  []string          `yaml:"strip_fields"`
	RenameKeys   map[string]string `yaml:"rename_keys"`
}

// envVarPattern matches ${VAR_NAME} placeholders in configuration values.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// LoadConfig reads the YAML file at path, performs environment-variable
// substitution, unmarshals it into a GatewayConfig, and applies sensible
// defaults for any values that were not explicitly set.
func LoadConfig(path string) (*GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Replace ${VAR} placeholders with environment variable values.
	resolved := envVarPattern.ReplaceAllStringFunc(string(data), func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		value, ok := os.LookupEnv(varName)
		if !ok {
			slog.Warn("environment variable not set, using empty string", "var", varName)
			return ""
		}
		return value
	})

	var cfg GatewayConfig
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	applyDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// applyDefaults fills in zero-valued fields with sensible defaults.
func applyDefaults(cfg *GatewayConfig) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.Server.ShutdownTimeout == 0 {
		cfg.Server.ShutdownTimeout = 15 * time.Second
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.RateLimit.Backend == "" {
		cfg.RateLimit.Backend = "memory"
	}

	if cfg.RateLimit.Backend == "redis" && cfg.RateLimit.Redis != nil {
		r := cfg.RateLimit.Redis
		if r.PoolSize == 0 {
			r.PoolSize = 10
		}
		if r.DialTimeout == 0 {
			r.DialTimeout = 5 * time.Second
		}
		if r.ReadTimeout == 0 {
			r.ReadTimeout = 3 * time.Second
		}
		if r.WriteTimeout == 0 {
			r.WriteTimeout = 3 * time.Second
		}
		if r.QueryTimeout == 0 {
			r.QueryTimeout = 100 * time.Millisecond
		}
	}

	for i := range cfg.Routes {
		if cfg.Routes[i].Upstream.Timeout == 0 {
			cfg.Routes[i].Upstream.Timeout = 30 * time.Second
		}
		if cfg.Routes[i].RateLimit != nil && cfg.Routes[i].RateLimit.Algorithm == "" {
			cfg.Routes[i].RateLimit.Algorithm = "sliding_window"
		}
	}
}

// Validate checks the configuration for common mistakes that would otherwise
// only surface at request time. It returns an error describing all problems
// found, or nil when the configuration is valid.
func (cfg *GatewayConfig) Validate() error {
	var errs []string

	for _, route := range cfg.Routes {
		// Validate path regex compiles.
		if route.Match.PathRegex != "" {
			if _, err := regexp.Compile(route.Match.PathRegex); err != nil {
				errs = append(errs, fmt.Sprintf("route %q: invalid path_regex %q: %v", route.Name, route.Match.PathRegex, err))
			}
		}

		if rl := route.RateLimit; rl != nil {
			if rl.RequestsPerWindow <= 0 {
				errs = append(errs, fmt.Sprintf("route %q: requests_per_window must be > 0, got %d", route.Name, rl.RequestsPerWindow))
			}
			if rl.Window <= 0 {
				errs = append(errs, fmt.Sprintf("route %q: window must be > 0, got %v", route.Name, rl.Window))
			}
			if rl.KeySource != "" && rl.KeySource != "ip" {
				if !strings.HasPrefix(rl.KeySource, "header:") || strings.TrimPrefix(rl.KeySource, "header:") == "" {
					errs = append(errs, fmt.Sprintf("route %q: invalid key_source %q (must be \"ip\", \"header:<name>\", or empty)", route.Name, rl.KeySource))
				}
			}
			if rl.Algorithm != "sliding_window" && rl.Algorithm != "leaky_bucket" {
				errs = append(errs, fmt.Sprintf("route %q: invalid algorithm %q (must be \"sliding_window\" or \"leaky_bucket\")", route.Name, rl.Algorithm))
			}
		}

		if route.Auth == nil {
			continue
		}
		providers := route.Auth.Providers
		if len(providers) == 0 {
			errs = append(errs, fmt.Sprintf("route %q: auth is set but no providers are configured", route.Name))
			continue
		}
		seen := make(map[string]bool, len(providers))

		for _, p := range providers {
			if p == "" {
				errs = append(errs, fmt.Sprintf("route %q: provider name is empty", route.Name))
				continue
			}
			if seen[p] {
				errs = append(errs, fmt.Sprintf("route %q: duplicate provider %q", route.Name, p))
				continue
			}
			seen[p] = true

			if p != "none" {
				if _, ok := cfg.AuthProviders[p]; !ok {
					errs = append(errs, fmt.Sprintf("route %q: provider %q not defined in auth_providers", route.Name, p))
				}
			}
		}

		if seen["none"] && len(providers) > 1 {
			errs = append(errs, fmt.Sprintf("route %q: \"none\" cannot be combined with other providers", route.Name))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// NonReloadableChanges compares old and new configurations and returns a list
// of human-readable warnings for non-reloadable fields that differ. These
// fields require a full restart to take effect.
func NonReloadableChanges(old, new *GatewayConfig) []string {
	var warnings []string

	if old.Server.Host != new.Server.Host {
		warnings = append(warnings, fmt.Sprintf("server.host changed (%q -> %q) — requires restart", old.Server.Host, new.Server.Host))
	}
	if old.Server.Port != new.Server.Port {
		warnings = append(warnings, fmt.Sprintf("server.port changed (%d -> %d) — requires restart", old.Server.Port, new.Server.Port))
	}
	if old.Server.ReadTimeout != new.Server.ReadTimeout {
		warnings = append(warnings, fmt.Sprintf("server.read_timeout changed (%v -> %v) — requires restart", old.Server.ReadTimeout, new.Server.ReadTimeout))
	}
	if old.Server.WriteTimeout != new.Server.WriteTimeout {
		warnings = append(warnings, fmt.Sprintf("server.write_timeout changed (%v -> %v) — requires restart", old.Server.WriteTimeout, new.Server.WriteTimeout))
	}
	if old.Server.IdleTimeout != new.Server.IdleTimeout {
		warnings = append(warnings, fmt.Sprintf("server.idle_timeout changed (%v -> %v) — requires restart", old.Server.IdleTimeout, new.Server.IdleTimeout))
	}
	if old.Server.ShutdownTimeout != new.Server.ShutdownTimeout {
		warnings = append(warnings, fmt.Sprintf("server.shutdown_timeout changed (%v -> %v) — requires restart", old.Server.ShutdownTimeout, new.Server.ShutdownTimeout))
	}
	if !stringSliceEqual(old.Server.TrustedProxies, new.Server.TrustedProxies) {
		warnings = append(warnings, "server.trusted_proxies changed — requires restart")
	}

	if old.RateLimit.Backend != new.RateLimit.Backend {
		warnings = append(warnings, fmt.Sprintf("rate_limit.backend changed (%q -> %q) — requires restart", old.RateLimit.Backend, new.RateLimit.Backend))
	}
	if !redisConfigEqual(old.RateLimit.Redis, new.RateLimit.Redis) {
		warnings = append(warnings, "rate_limit.redis settings changed — requires restart")
	}

	if !authProvidersEqual(old.AuthProviders, new.AuthProviders) {
		warnings = append(warnings, "auth_providers definitions changed — requires restart")
	}

	if old.Logging.Level != new.Logging.Level {
		warnings = append(warnings, fmt.Sprintf("logging.level changed (%q -> %q) — requires restart", old.Logging.Level, new.Logging.Level))
	}

	return warnings
}

func redisConfigEqual(a, b *RedisConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Addr == b.Addr && a.Password == b.Password && a.DB == b.DB &&
		a.PoolSize == b.PoolSize && a.MinIdleConns == b.MinIdleConns &&
		a.DialTimeout == b.DialTimeout && a.ReadTimeout == b.ReadTimeout &&
		a.WriteTimeout == b.WriteTimeout && a.MaxRetries == b.MaxRetries &&
		a.QueryTimeout == b.QueryTimeout && a.TLSEnabled == b.TLSEnabled
}

func authProvidersEqual(a, b map[string]AuthProvider) bool {
	if len(a) != len(b) {
		return false
	}
	for name, ap := range a {
		bp, ok := b[name]
		if !ok {
			return false
		}
		if ap.Type != bp.Type {
			return false
		}
		// Compare sub-configs by presence (definition changes need restart).
		if !jwtConfigEqual(ap.JWT, bp.JWT) {
			return false
		}
		if !apiKeyConfigEqual(ap.APIKey, bp.APIKey) {
			return false
		}
		if !oidcConfigEqual(ap.OIDC, bp.OIDC) {
			return false
		}
	}
	return true
}

func jwtConfigEqual(a, b *JWTConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Secret == b.Secret && a.PublicKeyFile == b.PublicKeyFile &&
		a.Algorithm == b.Algorithm && a.Issuer == b.Issuer && a.Audience == b.Audience
}

func apiKeyConfigEqual(a, b *APIKeyConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Header != b.Header {
		return false
	}
	if len(a.Keys) != len(b.Keys) {
		return false
	}
	for i := range a.Keys {
		if a.Keys[i].Key != b.Keys[i].Key || a.Keys[i].Name != b.Keys[i].Name {
			return false
		}
	}
	return true
}

func oidcConfigEqual(a, b *OIDCConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.IssuerURL != b.IssuerURL || a.JWKSURL != b.JWKSURL ||
		a.IntrospectionURL != b.IntrospectionURL ||
		a.ClientID != b.ClientID || a.ClientSecret != b.ClientSecret ||
		a.Audience != b.Audience || a.SkipIssuerCheck != b.SkipIssuerCheck {
		return false
	}
	if len(a.AllowedAlgorithms) != len(b.AllowedAlgorithms) {
		return false
	}
	for i := range a.AllowedAlgorithms {
		if a.AllowedAlgorithms[i] != b.AllowedAlgorithms[i] {
			return false
		}
	}
	return true
}

// DiffSummary returns a human-readable list of reloadable changes between
// old and new configurations, suitable for logging after a successful reload.
func DiffSummary(old, new *GatewayConfig) []string {
	var changes []string

	// Index old routes by name.
	oldRoutes := make(map[string]*RouteConfig, len(old.Routes))
	for i := range old.Routes {
		oldRoutes[old.Routes[i].Name] = &old.Routes[i]
	}

	// Detect added and modified routes.
	newRoutes := make(map[string]*RouteConfig, len(new.Routes))
	for i := range new.Routes {
		nr := &new.Routes[i]
		newRoutes[nr.Name] = nr

		or, existed := oldRoutes[nr.Name]
		if !existed {
			changes = append(changes, fmt.Sprintf("route %q: added", nr.Name))
			continue
		}
		if routeChanged(or, nr) {
			changes = append(changes, fmt.Sprintf("route %q: modified", nr.Name))
		}
	}

	// Detect removed routes.
	for _, or := range old.Routes {
		if _, exists := newRoutes[or.Name]; !exists {
			changes = append(changes, fmt.Sprintf("route %q: removed", or.Name))
		}
	}

	// Route order changes (affects first-match semantics).
	if len(old.Routes) == len(new.Routes) && len(changes) == 0 {
		for i := range old.Routes {
			if old.Routes[i].Name != new.Routes[i].Name {
				changes = append(changes, "route evaluation order changed")
				break
			}
		}
	}

	// CORS changes.
	if corsChanged(&old.CORS, &new.CORS) {
		changes = append(changes, "global CORS configuration changed")
	}

	return changes
}

func routeChanged(a, b *RouteConfig) bool {
	if a.Match.PathRegex != b.Match.PathRegex {
		return true
	}
	if !stringSliceEqual(a.Match.Methods, b.Match.Methods) {
		return true
	}
	if a.Upstream.URL != b.Upstream.URL || a.Upstream.PathRewrite != b.Upstream.PathRewrite || a.Upstream.Timeout != b.Upstream.Timeout {
		return true
	}
	if !routeAuthEqual(a.Auth, b.Auth) {
		return true
	}
	if !routeLimitEqual(a.RateLimit, b.RateLimit) {
		return true
	}
	// Consider any change in optional config blocks as a modification.
	if !retryEqual(a.Retry, b.Retry) {
		return true
	}
	if !cbEqual(a.CircuitBreaker, b.CircuitBreaker) {
		return true
	}
	if corsChanged(a.CORS, b.CORS) {
		return true
	}
	if transformChanged(a.Transform, b.Transform) {
		return true
	}
	return false
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func routeAuthEqual(a, b *RouteAuthConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return stringSliceEqual(a.Providers, b.Providers)
}

func routeLimitEqual(a, b *RouteLimitConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.RequestsPerWindow == b.RequestsPerWindow && a.Window == b.Window && a.KeySource == b.KeySource && a.Algorithm == b.Algorithm
}

func retryEqual(a, b *RetryConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.MaxRetries != b.MaxRetries || a.InitialDelay != b.InitialDelay || a.Multiplier != b.Multiplier {
		return false
	}
	if len(a.RetryableStatusCodes) != len(b.RetryableStatusCodes) {
		return false
	}
	for i := range a.RetryableStatusCodes {
		if a.RetryableStatusCodes[i] != b.RetryableStatusCodes[i] {
			return false
		}
	}
	return true
}

func cbEqual(a, b *CircuitBreakerConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.FailureThreshold == b.FailureThreshold && a.SuccessThreshold == b.SuccessThreshold && a.Timeout == b.Timeout
}

func transformChanged(a, b *TransformConfig) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	if a.MaxBodySize != b.MaxBodySize {
		return true
	}
	if directionTransformChanged(a.Request, b.Request) {
		return true
	}
	if directionTransformChanged(a.Response, b.Response) {
		return true
	}
	return false
}

func directionTransformChanged(a, b *DirectionTransform) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	if headerTransformChanged(a.Headers, b.Headers) {
		return true
	}
	if bodyTransformChanged(a.Body, b.Body) {
		return true
	}
	return false
}

func headerTransformChanged(a, b *HeaderTransform) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	if len(a.Add) != len(b.Add) {
		return true
	}
	for k, v := range a.Add {
		if b.Add[k] != v {
			return true
		}
	}
	if !stringSliceEqual(a.Remove, b.Remove) {
		return true
	}
	if len(a.Rename) != len(b.Rename) {
		return true
	}
	for k, v := range a.Rename {
		if b.Rename[k] != v {
			return true
		}
	}
	return false
}

func bodyTransformChanged(a, b *BodyTransform) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	if len(a.InjectFields) != len(b.InjectFields) {
		return true
	}
	for k, v := range a.InjectFields {
		bv, ok := b.InjectFields[k]
		if !ok || fmt.Sprintf("%v", v) != fmt.Sprintf("%v", bv) {
			return true
		}
	}
	if !stringSliceEqual(a.StripFields, b.StripFields) {
		return true
	}
	if len(a.RenameKeys) != len(b.RenameKeys) {
		return true
	}
	for k, v := range a.RenameKeys {
		if b.RenameKeys[k] != v {
			return true
		}
	}
	return false
}

func corsChanged(a, b *CORSConfig) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	if !stringSliceEqual(a.AllowedOrigins, b.AllowedOrigins) {
		return true
	}
	if !stringSliceEqual(a.AllowedMethods, b.AllowedMethods) {
		return true
	}
	if !stringSliceEqual(a.AllowedHeaders, b.AllowedHeaders) {
		return true
	}
	if !stringSliceEqual(a.ExposedHeaders, b.ExposedHeaders) {
		return true
	}
	if a.AllowCredentials != b.AllowCredentials || a.MaxAge != b.MaxAge {
		return true
	}
	return false
}
