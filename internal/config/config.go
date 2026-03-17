package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MiddlewareDefinition describes a reusable middleware configuration.
type MiddlewareDefinition struct {
	Type   string    `yaml:"type" jsonschema:"required,enum=cors,enum=rate_limit,enum=auth,enum=transform"`
	Config yaml.Node `yaml:"config"`
}

// MiddlewareRef references a middleware definition, with optional config overrides.
type MiddlewareRef struct {
	Ref    string    `yaml:"ref" jsonschema:"required"`
	Config yaml.Node `yaml:"config,omitempty"`
}

// RateLimitConfig holds per-middleware rate-limiting settings.
// Replaces RouteLimitConfig with renamed fields.
type RateLimitConfig struct {
	Requests  int           `yaml:"requests" jsonschema:"minimum=1,description=Max requests allowed per window"`
	Window    time.Duration `yaml:"window" jsonschema:"description=Rate limit window duration"`
	KeySource string        `yaml:"key_source" jsonschema:"description=Key source: ip or header:<name>"`
	Algorithm string        `yaml:"algorithm" jsonschema:"default=sliding_window,enum=sliding_window,enum=leaky_bucket"`
}

// AuthMiddlewareConfig selects authentication providers for a middleware.
type AuthMiddlewareConfig struct {
	Providers []string `yaml:"providers"`
}

// ResolvedMiddleware is the result of resolving a MiddlewareRef against definitions.
type ResolvedMiddleware struct {
	Name   string // definition name
	Type   string // middleware type
	Config any    // typed config
}


// TracingConfig holds distributed tracing settings.
type TracingConfig struct {
	Enabled     bool    `yaml:"enabled" jsonschema:"default=false,description=Enable distributed tracing"`
	Exporter    string  `yaml:"exporter" jsonschema:"default=otlp,enum=otlp,enum=stdout,description=Trace exporter type"`
	Endpoint    string  `yaml:"endpoint" jsonschema:"description=OTLP collector endpoint (e.g. localhost:4317)"`
	SampleRate  float64 `yaml:"sample_rate" jsonschema:"default=1.0,minimum=0,maximum=1,description=Sampling rate (0.0 to 1.0)"`
	ServiceName string  `yaml:"service_name" jsonschema:"default=tanugate,description=Service name reported in traces"`
	Insecure    bool    `yaml:"insecure" jsonschema:"default=false,description=Use insecure connection to collector"`
}
// GatewayConfig is the top-level configuration for the API gateway.
type GatewayConfig struct {
	Server                ServerConfig                       `yaml:"server" jsonschema:"description=HTTP server settings"`
	Logging               LoggingConfig                      `yaml:"logging" jsonschema:"description=Logging configuration"`
	Tracing               TracingConfig                      `yaml:"tracing" jsonschema:"description=Distributed tracing configuration"`
	RateLimit             RateLimitGlobalConfig              `yaml:"rate_limit" jsonschema:"description=Global rate limiting settings"`
	AuthProviders         map[string]AuthProvider            `yaml:"auth_providers" jsonschema:"description=Named authentication providers"`
	MiddlewareDefinitions map[string]MiddlewareDefinition    `yaml:"middleware_definitions" jsonschema:"description=Reusable middleware definitions"`
	Middlewares           []MiddlewareRef                    `yaml:"middlewares" jsonschema:"description=Global middleware chain applied to all routes"`
	Routes                []RouteConfig                      `yaml:"routes" jsonschema:"required,description=Route definitions"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host            string        `yaml:"host" jsonschema:"default=0.0.0.0,description=Bind address"`
	Port            int           `yaml:"port" jsonschema:"default=8080,minimum=1,maximum=65535,description=Listen port"`
	ReadTimeout     time.Duration `yaml:"read_timeout" jsonschema:"description=Max time to read the entire request (e.g. 30s)"`
	WriteTimeout    time.Duration `yaml:"write_timeout" jsonschema:"description=Max time to write the response (e.g. 30s)"`
	IdleTimeout     time.Duration `yaml:"idle_timeout" jsonschema:"description=Max time for idle keep-alive connections (e.g. 120s)"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" jsonschema:"description=Graceful shutdown timeout (e.g. 15s)"`
	TrustedProxies  []string      `yaml:"trusted_proxies" jsonschema:"description=CIDR ranges or IPs of trusted reverse proxies"`
	MaxHeaderBytes  int           `yaml:"max_header_bytes" jsonschema:"default=1048576,minimum=4096,description=Maximum size of request headers in bytes"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `yaml:"level" jsonschema:"default=info,enum=debug,enum=info,enum=warn,enum=error,description=Log level"`
}

// CORSConfig holds cross-origin resource sharing settings.
type CORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins" jsonschema:"description=Origins allowed to make cross-origin requests"`
	AllowedMethods   []string `yaml:"allowed_methods" jsonschema:"description=HTTP methods allowed for CORS requests"`
	AllowedHeaders   []string `yaml:"allowed_headers" jsonschema:"description=Headers allowed in CORS requests"`
	ExposedHeaders   []string `yaml:"exposed_headers" jsonschema:"description=Headers exposed to the browser"`
	AllowCredentials bool     `yaml:"allow_credentials" jsonschema:"description=Whether credentials are allowed"`
	MaxAge           int      `yaml:"max_age" jsonschema:"minimum=0,description=Max age in seconds for preflight cache"`
}

// RateLimitGlobalConfig holds global rate-limiting settings.
type RateLimitGlobalConfig struct {
	Backend string       `yaml:"backend" jsonschema:"default=memory,enum=memory,enum=redis,description=Rate limit storage backend"`
	Redis   *RedisConfig `yaml:"redis" jsonschema:"description=Redis connection settings (required when backend is redis)"`
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	Addr         string        `yaml:"addr" jsonschema:"required,description=Redis address (host:port)"`
	Password     string        `yaml:"password" jsonschema:"description=Redis password"`
	DB           int           `yaml:"db" jsonschema:"minimum=0,description=Redis database number"`
	PoolSize     int           `yaml:"pool_size" jsonschema:"default=10,minimum=0,description=Connection pool size"`
	MinIdleConns int           `yaml:"min_idle_conns" jsonschema:"minimum=0,description=Minimum idle connections"`
	DialTimeout  time.Duration `yaml:"dial_timeout" jsonschema:"description=Connection dial timeout (e.g. 5s)"`
	ReadTimeout  time.Duration `yaml:"read_timeout" jsonschema:"description=Read timeout (e.g. 3s)"`
	WriteTimeout time.Duration `yaml:"write_timeout" jsonschema:"description=Write timeout (e.g. 3s)"`
	MaxRetries   int           `yaml:"max_retries" jsonschema:"minimum=0,description=Max retries on failure"`
	QueryTimeout time.Duration `yaml:"query_timeout" jsonschema:"description=Query timeout (e.g. 100ms)"`
	TLSEnabled   bool          `yaml:"tls_enabled" jsonschema:"description=Enable TLS for Redis connection"`
}

// AuthProvider describes a single authentication provider.
type AuthProvider struct {
	Type   string        `yaml:"type" jsonschema:"required,enum=jwt,enum=apikey,enum=oidc,description=Authentication provider type"`
	JWT    *JWTConfig    `yaml:"jwt" jsonschema:"description=JWT authentication settings"`
	APIKey *APIKeyConfig `yaml:"api_key" jsonschema:"description=API key authentication settings"`
	OIDC   *OIDCConfig   `yaml:"oidc" jsonschema:"description=OpenID Connect settings"`
}

// JWTConfig holds JWT authentication settings.
type JWTConfig struct {
	Secret        string `yaml:"secret" jsonschema:"description=HMAC shared secret"`
	PublicKeyFile string `yaml:"public_key_file" jsonschema:"description=Path to RSA/ECDSA public key file"`
	Algorithm     string `yaml:"algorithm" jsonschema:"enum=HS256,enum=HS384,enum=HS512,enum=RS256,enum=RS384,enum=RS512,enum=ES256,enum=ES384,enum=ES512,description=JWT signing algorithm"`
	Issuer        string `yaml:"issuer" jsonschema:"description=Expected token issuer"`
	Audience      string `yaml:"audience" jsonschema:"description=Expected token audience"`
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
	Name            string                `yaml:"name" jsonschema:"required"`
	Match           MatchConfig           `yaml:"match" jsonschema:"required"`
	Upstream        UpstreamConfig        `yaml:"upstream" jsonschema:"required"`
	Retry           *RetryConfig          `yaml:"retry"`
	CircuitBreaker  *CircuitBreakerConfig `yaml:"circuit_breaker"`
	SkipMiddlewares []string              `yaml:"skip_middlewares" jsonschema:"description=Names of global middlewares to skip for this route"`
	Middlewares     []MiddlewareRef       `yaml:"middlewares" jsonschema:"description=Ordered middleware refs for this route"`
}

// MatchConfig holds request-matching criteria for a route.
type MatchConfig struct {
	PathRegex string            `yaml:"path_regex" jsonschema:"required"`
	Methods   []string          `yaml:"methods"`
	Host      string            `yaml:"host" jsonschema:"description=Host to match against (exact or wildcard e.g. *.example.com)"`
	Headers   map[string]string `yaml:"headers" jsonschema:"description=Header matching rules (name -> regex pattern or * for presence-only)"`
}

// UpstreamConfig holds upstream target settings for a route.
type UpstreamConfig struct {
	URL         string        `yaml:"url" jsonschema:"required"`
	PathRewrite string        `yaml:"path_rewrite"`
	Timeout     time.Duration `yaml:"timeout"`
}

// RetryConfig holds retry behaviour settings for a route.
type RetryConfig struct {
	MaxRetries           int           `yaml:"max_retries" jsonschema:"minimum=1,description=Maximum number of retry attempts"`
	InitialDelay         time.Duration `yaml:"initial_delay" jsonschema:"description=Initial delay before first retry (e.g. 100ms)"`
	Multiplier           float64       `yaml:"multiplier" jsonschema:"minimum=1,description=Backoff multiplier for subsequent retries"`
	RetryableStatusCodes []int         `yaml:"retryable_status_codes" jsonschema:"description=HTTP status codes that trigger a retry"`
}

// CircuitBreakerConfig holds circuit-breaker settings for a route.
type CircuitBreakerConfig struct {
	FailureThreshold int           `yaml:"failure_threshold" jsonschema:"minimum=1,description=Failures before opening the circuit"`
	SuccessThreshold int           `yaml:"success_threshold" jsonschema:"minimum=1,description=Successes in half-open state before closing"`
	Timeout          time.Duration `yaml:"timeout" jsonschema:"description=Time to wait before transitioning to half-open (e.g. 30s)"`
}

// TransformConfig holds request/response transformation rules for a route.
type TransformConfig struct {
	Request     *DirectionTransform `yaml:"request" jsonschema:"description=Request transformations"`
	Response    *DirectionTransform `yaml:"response" jsonschema:"description=Response transformations"`
	MaxBodySize int64               `yaml:"max_body_size" jsonschema:"minimum=0,description=Maximum body size in bytes for transformation"`
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
			slog.Warn("environment variable not set, leaving placeholder", "var", varName)
			return match
		}
		return value
	})

	// Validate raw YAML against schema before unmarshaling.
	// This catches unknown fields (typos) and invalid values before defaults.
	if schemaErrs := ValidateRawConfig([]byte(resolved)); len(schemaErrs) > 0 {
		return nil, fmt.Errorf("config validation failed: %s", strings.Join(schemaErrs, "; "))
	}

	var cfg GatewayConfig
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	applyDefaults(&cfg)

	// Semantic validation on post-defaults struct.
	if semanticErrs := cfg.semanticErrors(); len(semanticErrs) > 0 {
		return nil, fmt.Errorf("config validation failed: %s", strings.Join(semanticErrs, "; "))
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
	if cfg.Server.MaxHeaderBytes == 0 {
		cfg.Server.MaxHeaderBytes = 1 << 20 // 1 MB
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.RateLimit.Backend == "" {
		cfg.RateLimit.Backend = "memory"
	}
	if cfg.Tracing.Exporter == "" {
		cfg.Tracing.Exporter = "otlp"
	}
	if cfg.Tracing.SampleRate == 0 {
		cfg.Tracing.SampleRate = 1.0
	}
	if cfg.Tracing.ServiceName == "" {
		cfg.Tracing.ServiceName = "tanugate"
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
	}
}

// Validate checks the configuration for common mistakes that would otherwise
// only surface at request time. It returns an error describing all problems
// found, or nil when the configuration is valid.
func (cfg *GatewayConfig) Validate() error {
	var errs []string
	errs = append(errs, validateAgainstSchema(cfg)...)
	errs = append(errs, cfg.semanticErrors()...)
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// hasUnresolvedEnvVar reports whether s still contains a ${VAR} placeholder.
func hasUnresolvedEnvVar(s string) bool {
	return envVarPattern.MatchString(s)
}

// semanticErrors returns human-readable error strings for logic errors
// that the JSON Schema cannot express (regex compilation, middleware
// resolution, auth provider references, etc.).
func (cfg *GatewayConfig) semanticErrors() []string {
	var errs []string

	// Validate tracing exporter value.
	switch cfg.Tracing.Exporter {
	case "otlp", "stdout":
		// valid
	default:
		errs = append(errs, fmt.Sprintf("tracing.exporter %q is not valid, must be \"otlp\" or \"stdout\"", cfg.Tracing.Exporter))
	}

	// Duplicate route names cause silent handler overwrites in the pipeline builder.
	routeNames := make(map[string]bool, len(cfg.Routes))
	for _, route := range cfg.Routes {
		if routeNames[route.Name] {
			errs = append(errs, fmt.Sprintf("duplicate route name %q", route.Name))
		}
		routeNames[route.Name] = true
	}

	// Sensitive fields must not contain unresolved env var placeholders.
	for name, ap := range cfg.AuthProviders {
		if ap.JWT != nil && hasUnresolvedEnvVar(ap.JWT.Secret) {
			errs = append(errs, fmt.Sprintf("auth_providers.%s.jwt.secret contains unresolved env var placeholder %q — set the variable or use a literal value", name, ap.JWT.Secret))
		}
		if ap.APIKey != nil {
			for i, k := range ap.APIKey.Keys {
				if hasUnresolvedEnvVar(k.Key) {
					errs = append(errs, fmt.Sprintf("auth_providers.%s.api_key.keys[%d].key contains unresolved env var placeholder %q — set the variable or use a literal value", name, i, k.Key))
				}
			}
		}
		if ap.OIDC != nil && hasUnresolvedEnvVar(ap.OIDC.ClientSecret) {
			errs = append(errs, fmt.Sprintf("auth_providers.%s.oidc.client_secret contains unresolved env var placeholder %q — set the variable or use a literal value", name, ap.OIDC.ClientSecret))
		}
	}
	if cfg.RateLimit.Redis != nil {
		if hasUnresolvedEnvVar(cfg.RateLimit.Redis.Addr) {
			errs = append(errs, fmt.Sprintf("rate_limit.redis.addr contains unresolved env var placeholder %q — set the variable or use a literal value", cfg.RateLimit.Redis.Addr))
		}
		if hasUnresolvedEnvVar(cfg.RateLimit.Redis.Password) {
			errs = append(errs, fmt.Sprintf("rate_limit.redis.password contains unresolved env var placeholder %q — set the variable or use a literal value", cfg.RateLimit.Redis.Password))
		}
	}

	// Validate global middleware refs resolve.
	globalResolved, globalErr := ResolveMiddlewares(cfg.Middlewares, cfg.MiddlewareDefinitions)
	if globalErr != nil {
		errs = append(errs, fmt.Sprintf("global middlewares: %v", globalErr))
	}

	// Per-type validation for global resolved middlewares.
	if globalErr == nil {
		errs = append(errs, validateResolvedMiddlewares("global", globalResolved, cfg)...)
	}

	// Build a set of global middleware ref names for skip_middlewares validation.
	globalRefNames := make(map[string]bool, len(cfg.Middlewares))
	for _, ref := range cfg.Middlewares {
		globalRefNames[ref.Ref] = true
	}

	for _, route := range cfg.Routes {
		if route.Match.PathRegex != "" {
			if _, err := regexp.Compile(route.Match.PathRegex); err != nil {
				errs = append(errs, fmt.Sprintf("route %q: invalid path_regex %q: %v", route.Name, route.Match.PathRegex, err))
			}
		}

		if h := route.Match.Host; h != "" {
			if err := validateHostPattern(h); err != nil {
				errs = append(errs, fmt.Sprintf("route %q: invalid host %q: %v", route.Name, h, err))
			}
		}

		for hdrName, pattern := range route.Match.Headers {
			if pattern == "*" {
				continue // presence-only, no regex to compile
			}
			anchored := "^(?:" + pattern + ")$"
			if _, err := regexp.Compile(anchored); err != nil {
				errs = append(errs, fmt.Sprintf("route %q: invalid header regex for %q: %q: %v", route.Name, hdrName, pattern, err))
			}
		}

		// Validate skip_middlewares entries reference global middleware names.
		for _, name := range route.SkipMiddlewares {
			if !globalRefNames[name] {
				errs = append(errs, fmt.Sprintf("route %q: skip_middlewares entry %q does not match any global middleware ref", route.Name, name))
			}
		}

		// Resolve route-level middleware refs.
		routeResolved, routeErr := ResolveMiddlewares(route.Middlewares, cfg.MiddlewareDefinitions)
		if routeErr != nil {
			errs = append(errs, fmt.Sprintf("route %q: %v", route.Name, routeErr))
			continue
		}

		// Per-type validation for route-level resolved middlewares.
		errs = append(errs, validateResolvedMiddlewares(fmt.Sprintf("route %q", route.Name), routeResolved, cfg)...)

		// Build the effective chain: filtered globals + route middlewares.
		skipSet := make(map[string]bool, len(route.SkipMiddlewares))
		for _, name := range route.SkipMiddlewares {
			skipSet[name] = true
		}
		var effectiveChain []ResolvedMiddleware
		if globalErr == nil {
			for _, gm := range globalResolved {
				if !skipSet[gm.Name] {
					effectiveChain = append(effectiveChain, gm)
				}
			}
		}
		effectiveChain = append(effectiveChain, routeResolved...)

		// Validate CORS: at most one, must be first.
		corsCount := 0
		for i, mw := range effectiveChain {
			if mw.Type == "cors" {
				corsCount++
				if i != 0 && corsCount == 1 {
					errs = append(errs, fmt.Sprintf("route %q: CORS middleware %q must be first in the middleware chain, but is at position %d", route.Name, mw.Name, i))
				}
			}
		}
		if corsCount > 1 {
			errs = append(errs, fmt.Sprintf("route %q: %d CORS middlewares in effective chain; use skip_middlewares to remove the global one before adding a route-level override", route.Name, corsCount))
		}

		// Validate rate_limit with claim: key_source has preceding auth.
		authSeen := false
		for _, mw := range effectiveChain {
			if mw.Type == "auth" {
				authSeen = true
			}
			if mw.Type == "rate_limit" {
				if rlCfg, ok := mw.Config.(*RateLimitConfig); ok {
					if strings.HasPrefix(rlCfg.KeySource, "claim:") && !authSeen {
						errs = append(errs, fmt.Sprintf("route %q: rate_limit middleware %q uses key_source %q but no auth middleware precedes it", route.Name, mw.Name, rlCfg.KeySource))
					}
				}
			}
		}
	}

	return errs
}

// validateHostPattern checks that a host value is either a plain hostname or a
// valid single-level wildcard of the form *.suffix. It rejects malformed
// patterns like *.*.com, *example.com, and api.*.com.
func validateHostPattern(host string) error {
	if strings.Contains(host, "*") {
		if !strings.HasPrefix(host, "*.") {
			return fmt.Errorf("wildcard must start with '*.' (e.g. *.example.com)")
		}
		suffix := host[2:] // strip "*."
		if suffix == "" {
			return fmt.Errorf("wildcard suffix must not be empty")
		}
		if strings.Contains(suffix, "*") {
			return fmt.Errorf("only a single leading wildcard is supported (e.g. *.example.com)")
		}
		// Suffix must have at least one dot (e.g. "example.com", not just "com").
		if !strings.Contains(suffix, ".") {
			return fmt.Errorf("wildcard suffix must contain at least two labels (e.g. *.example.com, not *.com)")
		}
		for _, label := range strings.Split(suffix, ".") {
			if label == "" {
				return fmt.Errorf("host contains empty label")
			}
		}
		return nil
	}
	// Exact host: no wildcards. Validate it's not empty and has no empty labels.
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return fmt.Errorf("host contains empty label")
		}
	}
	return nil
}

// validateResolvedMiddlewares performs per-type validation on a set of resolved
// middlewares and returns error strings prefixed with context (e.g. "global" or
// "route \"name\"").
func validateResolvedMiddlewares(context string, resolved []ResolvedMiddleware, cfg *GatewayConfig) []string {
	var errs []string
	for _, mw := range resolved {
		switch mw.Type {
		case "rate_limit":
			rlCfg, ok := mw.Config.(*RateLimitConfig)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: internal error: expected *RateLimitConfig, got %T", context, mw.Name, mw.Config))
				continue
			}
			if rlCfg.Requests <= 0 {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: requests must be > 0, got %d", context, mw.Name, rlCfg.Requests))
			}
			if rlCfg.Window <= 0 {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: window must be > 0, got %v", context, mw.Name, rlCfg.Window))
			}
			switch rlCfg.Algorithm {
			case "sliding_window", "leaky_bucket":
				// valid
			default:
				errs = append(errs, fmt.Sprintf("%s: middleware %q: unsupported algorithm %q, must be sliding_window or leaky_bucket", context, mw.Name, rlCfg.Algorithm))
			}
			if rlCfg.Algorithm == "leaky_bucket" && cfg.RateLimit.Backend != "redis" {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: algorithm %q requires rate_limit.backend \"redis\", got %q",
					context, mw.Name, rlCfg.Algorithm, cfg.RateLimit.Backend))
			}
			if rlCfg.KeySource != "" && rlCfg.KeySource != "ip" {
				if !strings.HasPrefix(rlCfg.KeySource, "header:") && !strings.HasPrefix(rlCfg.KeySource, "claim:") {
					errs = append(errs, fmt.Sprintf("%s: middleware %q: invalid key_source %q (must be \"ip\", \"header:<name>\", \"claim:<name>\", or empty)", context, mw.Name, rlCfg.KeySource))
				} else if strings.HasPrefix(rlCfg.KeySource, "header:") && strings.TrimPrefix(rlCfg.KeySource, "header:") == "" {
					errs = append(errs, fmt.Sprintf("%s: middleware %q: invalid key_source %q (header name is empty)", context, mw.Name, rlCfg.KeySource))
				} else if strings.HasPrefix(rlCfg.KeySource, "claim:") && strings.TrimPrefix(rlCfg.KeySource, "claim:") == "" {
					errs = append(errs, fmt.Sprintf("%s: middleware %q: invalid key_source %q (claim name is empty)", context, mw.Name, rlCfg.KeySource))
				}
			}

		case "auth":
			authCfg, ok := mw.Config.(*AuthMiddlewareConfig)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: internal error: expected *AuthMiddlewareConfig, got %T", context, mw.Name, mw.Config))
				continue
			}
			if len(authCfg.Providers) == 0 {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: auth has no providers configured", context, mw.Name))
				continue
			}
			seen := make(map[string]bool, len(authCfg.Providers))
			for _, p := range authCfg.Providers {
				if p == "" {
					errs = append(errs, fmt.Sprintf("%s: middleware %q: provider name is empty", context, mw.Name))
					continue
				}
				if seen[p] {
					errs = append(errs, fmt.Sprintf("%s: middleware %q: duplicate provider %q", context, mw.Name, p))
					continue
				}
				seen[p] = true
				if p != "none" {
					if _, ok := cfg.AuthProviders[p]; !ok {
						errs = append(errs, fmt.Sprintf("%s: middleware %q: provider %q not defined in auth_providers", context, mw.Name, p))
					}
				}
			}
			if seen["none"] && len(authCfg.Providers) > 1 {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: \"none\" cannot be combined with other providers", context, mw.Name))
			}

		case "cors":
			corsCfg, ok := mw.Config.(*CORSConfig)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: internal error: expected *CORSConfig, got %T", context, mw.Name, mw.Config))
				continue
			}
			if len(corsCfg.AllowedOrigins) == 0 {
				errs = append(errs, fmt.Sprintf("%s: middleware %q: cors has no allowed_origins configured — all cross-origin requests will be rejected", context, mw.Name))
			}
		}
	}
	return errs
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
	if old.Server.MaxHeaderBytes != new.Server.MaxHeaderBytes {
		warnings = append(warnings, fmt.Sprintf("server.max_header_bytes changed (%d -> %d) — requires restart", old.Server.MaxHeaderBytes, new.Server.MaxHeaderBytes))
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


	if !tracingConfigEqual(old.Tracing, new.Tracing) {
		warnings = append(warnings, "tracing settings changed — requires restart")
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

func tracingConfigEqual(a, b TracingConfig) bool {
	return a.Enabled == b.Enabled && a.Exporter == b.Exporter &&
		a.Endpoint == b.Endpoint && a.SampleRate == b.SampleRate &&
		a.ServiceName == b.ServiceName && a.Insecure == b.Insecure
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

	// Middleware definitions changes.
	changes = append(changes, diffMiddlewareDefinitions(old.MiddlewareDefinitions, new.MiddlewareDefinitions)...)

	// Global middlewares list changes.
	if !middlewareRefsEqual(old.Middlewares, new.Middlewares) {
		changes = append(changes, "global middlewares list changed")
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
	if a.Match.Host != b.Match.Host {
		return true
	}
	if !stringMapEqual(a.Match.Headers, b.Match.Headers) {
		return true
	}
	if a.Upstream.URL != b.Upstream.URL || a.Upstream.PathRewrite != b.Upstream.PathRewrite || a.Upstream.Timeout != b.Upstream.Timeout {
		return true
	}
	// Consider any change in optional config blocks as a modification.
	if !retryEqual(a.Retry, b.Retry) {
		return true
	}
	if !cbEqual(a.CircuitBreaker, b.CircuitBreaker) {
		return true
	}
	if !stringSliceEqual(a.SkipMiddlewares, b.SkipMiddlewares) {
		return true
	}
	if !middlewareRefsEqual(a.Middlewares, b.Middlewares) {
		return true
	}
	return false
}

func middlewareRefsEqual(a, b []MiddlewareRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Ref != b[i].Ref {
			return false
		}
		if !yamlNodeEqual(a[i].Config, b[i].Config) {
			return false
		}
	}
	return true
}

func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || v != bv {
			return false
		}
	}
	return true
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

// diffMiddlewareDefinitions returns human-readable change descriptions for
// added, removed, and modified middleware definitions.
func diffMiddlewareDefinitions(old, new map[string]MiddlewareDefinition) []string {
	var changes []string

	// Detect added and modified definitions.
	for name, nd := range new {
		od, existed := old[name]
		if !existed {
			changes = append(changes, fmt.Sprintf("middleware_definitions: %q added", name))
			continue
		}
		if !middlewareDefinitionEqual(od, nd) {
			changes = append(changes, fmt.Sprintf("middleware_definitions: %q modified", name))
		}
	}

	// Detect removed definitions.
	for name := range old {
		if _, exists := new[name]; !exists {
			changes = append(changes, fmt.Sprintf("middleware_definitions: %q removed", name))
		}
	}

	return changes
}

// middlewareDefinitionEqual compares two MiddlewareDefinition values by type
// and YAML node content using deep comparison via marshal-and-compare.
func middlewareDefinitionEqual(a, b MiddlewareDefinition) bool {
	if a.Type != b.Type {
		return false
	}
	return yamlNodeEqual(a.Config, b.Config)
}

// yamlNodeEqual compares two yaml.Node values by marshaling them to YAML bytes
// and comparing the results. This handles arbitrarily nested structures
// correctly, unlike shallow .Value comparison which misses nested mappings.
func yamlNodeEqual(a, b yaml.Node) bool {
	aBytes, err1 := yaml.Marshal(&a)
	bBytes, err2 := yaml.Marshal(&b)
	if err1 != nil || err2 != nil {
		return false // marshal failure → assume different (safe default)
	}
	return bytes.Equal(aBytes, bBytes)
}

