package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
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
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
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

// RouteAuthConfig selects the authentication provider for a route.
type RouteAuthConfig struct {
	Provider string `yaml:"provider"`
}

// RouteLimitConfig holds per-route rate-limiting settings.
type RouteLimitConfig struct {
	RequestsPerWindow int           `yaml:"requests_per_window"`
	Window            time.Duration `yaml:"window"`
	KeySource         string        `yaml:"key_source"`
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

	for i := range cfg.Routes {
		if cfg.Routes[i].Upstream.Timeout == 0 {
			cfg.Routes[i].Upstream.Timeout = 30 * time.Second
		}
	}
}
