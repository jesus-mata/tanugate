package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeConfig is a test helper that writes YAML to a temp file and returns the path.
func writeConfig(t *testing.T, yamlContent string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	return cfgPath
}

func TestLoadConfig_ValidFullConfig(t *testing.T) {
	yamlContent := `
server:
  host: "127.0.0.1"
  port: 9090
  read_timeout: 10s
  write_timeout: 20s
  idle_timeout: 60s
  shutdown_timeout: 5s

logging:
  level: "debug"

rate_limit:
  backend: "redis"
  redis:
    addr: "localhost:6379"
    password: "redispass"
    db: 2
    pool_size: 20
    min_idle_conns: 5
    dial_timeout: 10s
    read_timeout: 5s
    write_timeout: 5s
    max_retries: 3
    query_timeout: 200ms
    tls_enabled: true

auth_providers:
  main_jwt:
    type: "jwt"
    jwt:
      secret: "super-secret"
      public_key_file: "/keys/pub.pem"
      algorithm: "RS256"
      issuer: "https://auth.example.com"
      audience: "my-api"

middleware_definitions:
  jwt-auth:
    type: auth
    config:
      providers:
        - "main_jwt"
  ip-limiter:
    type: rate_limit
    config:
      requests: 100
      window: 1m
      key_source: "ip"
  my-transform:
    type: transform
    config:
      request:
        headers:
          add:
            X-Gateway: "true"
          remove:
            - "X-Internal"
          rename:
            X-Old: "X-New"
        body:
          inject_fields:
            source: "gateway"
          strip_fields:
            - "debug"
          rename_keys:
            old_key: "new_key"
      response:
        headers:
          add:
            X-Served-By: "api-gateway"
          remove:
            - "X-Debug"
          rename:
            X-Backend-Id: "X-Request-Id"
        body:
          strip_fields:
            - "internal_id"

routes:
  - name: "users-api"
    match:
      path_regex: "^/api/users"
      methods:
        - "GET"
        - "POST"
    upstream:
      url: "http://users-service:8081"
      path_rewrite: "/v1/users"
      timeout: 15s
    middlewares:
      - ref: jwt-auth
      - ref: ip-limiter
      - ref: my-transform
    retry:
      max_retries: 3
      initial_delay: 200ms
      multiplier: 2.0
      retryable_status_codes:
        - 502
        - 503
    circuit_breaker:
      failure_threshold: 5
      success_threshold: 2
      timeout: 30s
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	// Server
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "127.0.0.1")
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, 9090)
	}
	if cfg.Server.ReadTimeout != 10*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want %v", cfg.Server.ReadTimeout, 10*time.Second)
	}
	if cfg.Server.WriteTimeout != 20*time.Second {
		t.Errorf("Server.WriteTimeout = %v, want %v", cfg.Server.WriteTimeout, 20*time.Second)
	}
	if cfg.Server.IdleTimeout != 60*time.Second {
		t.Errorf("Server.IdleTimeout = %v, want %v", cfg.Server.IdleTimeout, 60*time.Second)
	}
	if cfg.Server.ShutdownTimeout != 5*time.Second {
		t.Errorf("Server.ShutdownTimeout = %v, want %v", cfg.Server.ShutdownTimeout, 5*time.Second)
	}

	// Logging
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}

	// RateLimit (global)
	if cfg.RateLimit.Backend != "redis" {
		t.Errorf("RateLimit.Backend = %q, want %q", cfg.RateLimit.Backend, "redis")
	}
	if cfg.RateLimit.Redis == nil {
		t.Fatal("RateLimit.Redis is nil, want non-nil")
	}
	if cfg.RateLimit.Redis.Addr != "localhost:6379" {
		t.Errorf("RateLimit.Redis.Addr = %q, want %q", cfg.RateLimit.Redis.Addr, "localhost:6379")
	}
	if cfg.RateLimit.Redis.Password != "redispass" {
		t.Errorf("RateLimit.Redis.Password = %q, want %q", cfg.RateLimit.Redis.Password, "redispass")
	}
	if cfg.RateLimit.Redis.DB != 2 {
		t.Errorf("RateLimit.Redis.DB = %d, want %d", cfg.RateLimit.Redis.DB, 2)
	}
	if cfg.RateLimit.Redis.PoolSize != 20 {
		t.Errorf("RateLimit.Redis.PoolSize = %d, want 20", cfg.RateLimit.Redis.PoolSize)
	}
	if cfg.RateLimit.Redis.MinIdleConns != 5 {
		t.Errorf("RateLimit.Redis.MinIdleConns = %d, want 5", cfg.RateLimit.Redis.MinIdleConns)
	}
	if cfg.RateLimit.Redis.DialTimeout != 10*time.Second {
		t.Errorf("RateLimit.Redis.DialTimeout = %v, want 10s", cfg.RateLimit.Redis.DialTimeout)
	}
	if cfg.RateLimit.Redis.ReadTimeout != 5*time.Second {
		t.Errorf("RateLimit.Redis.ReadTimeout = %v, want 5s", cfg.RateLimit.Redis.ReadTimeout)
	}
	if cfg.RateLimit.Redis.WriteTimeout != 5*time.Second {
		t.Errorf("RateLimit.Redis.WriteTimeout = %v, want 5s", cfg.RateLimit.Redis.WriteTimeout)
	}
	if cfg.RateLimit.Redis.MaxRetries != 3 {
		t.Errorf("RateLimit.Redis.MaxRetries = %d, want 3", cfg.RateLimit.Redis.MaxRetries)
	}
	if cfg.RateLimit.Redis.QueryTimeout != 200*time.Millisecond {
		t.Errorf("RateLimit.Redis.QueryTimeout = %v, want 200ms", cfg.RateLimit.Redis.QueryTimeout)
	}
	if !cfg.RateLimit.Redis.TLSEnabled {
		t.Errorf("RateLimit.Redis.TLSEnabled = false, want true")
	}

	// AuthProviders
	if len(cfg.AuthProviders) != 1 {
		t.Fatalf("AuthProviders length = %d, want 1", len(cfg.AuthProviders))
	}
	mainJWT, ok := cfg.AuthProviders["main_jwt"]
	if !ok {
		t.Fatal("AuthProviders[\"main_jwt\"] not found")
	}
	if mainJWT.Type != "jwt" {
		t.Errorf("AuthProviders[\"main_jwt\"].Type = %q, want %q", mainJWT.Type, "jwt")
	}
	if mainJWT.JWT == nil {
		t.Fatal("AuthProviders[\"main_jwt\"].JWT is nil, want non-nil")
	}
	if mainJWT.JWT.Secret != "super-secret" {
		t.Errorf("JWT.Secret = %q, want %q", mainJWT.JWT.Secret, "super-secret")
	}
	if mainJWT.JWT.PublicKeyFile != "/keys/pub.pem" {
		t.Errorf("JWT.PublicKeyFile = %q, want %q", mainJWT.JWT.PublicKeyFile, "/keys/pub.pem")
	}
	if mainJWT.JWT.Algorithm != "RS256" {
		t.Errorf("JWT.Algorithm = %q, want %q", mainJWT.JWT.Algorithm, "RS256")
	}
	if mainJWT.JWT.Issuer != "https://auth.example.com" {
		t.Errorf("JWT.Issuer = %q, want %q", mainJWT.JWT.Issuer, "https://auth.example.com")
	}
	if mainJWT.JWT.Audience != "my-api" {
		t.Errorf("JWT.Audience = %q, want %q", mainJWT.JWT.Audience, "my-api")
	}

	// MiddlewareDefinitions
	if len(cfg.MiddlewareDefinitions) != 3 {
		t.Fatalf("MiddlewareDefinitions length = %d, want 3", len(cfg.MiddlewareDefinitions))
	}
	if _, ok := cfg.MiddlewareDefinitions["jwt-auth"]; !ok {
		t.Error("MiddlewareDefinitions missing \"jwt-auth\"")
	}
	if _, ok := cfg.MiddlewareDefinitions["ip-limiter"]; !ok {
		t.Error("MiddlewareDefinitions missing \"ip-limiter\"")
	}
	if _, ok := cfg.MiddlewareDefinitions["my-transform"]; !ok {
		t.Error("MiddlewareDefinitions missing \"my-transform\"")
	}

	// Routes
	if len(cfg.Routes) != 1 {
		t.Fatalf("Routes length = %d, want 1", len(cfg.Routes))
	}
	route := cfg.Routes[0]
	if route.Name != "users-api" {
		t.Errorf("Route.Name = %q, want %q", route.Name, "users-api")
	}
	if route.Match.PathRegex != "^/api/users" {
		t.Errorf("Route.Match.PathRegex = %q, want %q", route.Match.PathRegex, "^/api/users")
	}
	if len(route.Match.Methods) != 2 || route.Match.Methods[0] != "GET" || route.Match.Methods[1] != "POST" {
		t.Errorf("Route.Match.Methods = %v, want [GET POST]", route.Match.Methods)
	}
	if route.Upstream.URL != "http://users-service:8081" {
		t.Errorf("Route.Upstream.URL = %q, want %q", route.Upstream.URL, "http://users-service:8081")
	}
	if route.Upstream.PathRewrite != "/v1/users" {
		t.Errorf("Route.Upstream.PathRewrite = %q, want %q", route.Upstream.PathRewrite, "/v1/users")
	}
	if route.Upstream.Timeout != 15*time.Second {
		t.Errorf("Route.Upstream.Timeout = %v, want %v", route.Upstream.Timeout, 15*time.Second)
	}

	// Route Middlewares refs
	if len(route.Middlewares) != 3 {
		t.Fatalf("Route.Middlewares length = %d, want 3", len(route.Middlewares))
	}
	if route.Middlewares[0].Ref != "jwt-auth" {
		t.Errorf("Route.Middlewares[0].Ref = %q, want %q", route.Middlewares[0].Ref, "jwt-auth")
	}
	if route.Middlewares[1].Ref != "ip-limiter" {
		t.Errorf("Route.Middlewares[1].Ref = %q, want %q", route.Middlewares[1].Ref, "ip-limiter")
	}
	if route.Middlewares[2].Ref != "my-transform" {
		t.Errorf("Route.Middlewares[2].Ref = %q, want %q", route.Middlewares[2].Ref, "my-transform")
	}

	// Resolve route middlewares and verify typed configs
	resolved, err := ResolveMiddlewares(route.Middlewares, cfg.MiddlewareDefinitions)
	if err != nil {
		t.Fatalf("ResolveMiddlewares error: %v", err)
	}
	if len(resolved) != 3 {
		t.Fatalf("resolved length = %d, want 3", len(resolved))
	}

	// Verify auth middleware config
	authMW := resolved[0]
	if authMW.Type != "auth" {
		t.Errorf("resolved[0].Type = %q, want %q", authMW.Type, "auth")
	}
	authCfg, ok := authMW.Config.(*AuthMiddlewareConfig)
	if !ok {
		t.Fatalf("resolved[0].Config is %T, want *AuthMiddlewareConfig", authMW.Config)
	}
	if len(authCfg.Providers) != 1 || authCfg.Providers[0] != "main_jwt" {
		t.Errorf("auth providers = %v, want [main_jwt]", authCfg.Providers)
	}

	// Verify rate_limit middleware config
	rlMW := resolved[1]
	if rlMW.Type != "rate_limit" {
		t.Errorf("resolved[1].Type = %q, want %q", rlMW.Type, "rate_limit")
	}
	rlCfg, ok := rlMW.Config.(*RateLimitConfig)
	if !ok {
		t.Fatalf("resolved[1].Config is %T, want *RateLimitConfig", rlMW.Config)
	}
	if rlCfg.Requests != 100 {
		t.Errorf("rate_limit.Requests = %d, want 100", rlCfg.Requests)
	}
	if rlCfg.Window != time.Minute {
		t.Errorf("rate_limit.Window = %v, want 1m", rlCfg.Window)
	}
	if rlCfg.KeySource != "ip" {
		t.Errorf("rate_limit.KeySource = %q, want %q", rlCfg.KeySource, "ip")
	}

	// Verify transform middleware config
	txMW := resolved[2]
	if txMW.Type != "transform" {
		t.Errorf("resolved[2].Type = %q, want %q", txMW.Type, "transform")
	}
	txCfg, ok := txMW.Config.(*TransformConfig)
	if !ok {
		t.Fatalf("resolved[2].Config is %T, want *TransformConfig", txMW.Config)
	}
	if txCfg.Request == nil {
		t.Fatal("transform.Request is nil, want non-nil")
	}
	if txCfg.Request.Headers == nil {
		t.Fatal("transform.Request.Headers is nil, want non-nil")
	}
	if v, ok := txCfg.Request.Headers.Add["X-Gateway"]; !ok || v != "true" {
		t.Errorf("Transform.Request.Headers.Add[\"X-Gateway\"] = %q, want %q", v, "true")
	}
	if len(txCfg.Request.Headers.Remove) != 1 || txCfg.Request.Headers.Remove[0] != "X-Internal" {
		t.Errorf("Transform.Request.Headers.Remove = %v, want [X-Internal]", txCfg.Request.Headers.Remove)
	}
	if v, ok := txCfg.Request.Headers.Rename["X-Old"]; !ok || v != "X-New" {
		t.Errorf("Transform.Request.Headers.Rename[\"X-Old\"] = %q, want %q", v, "X-New")
	}
	if txCfg.Request.Body == nil {
		t.Fatal("transform.Request.Body is nil, want non-nil")
	}
	if v, ok := txCfg.Request.Body.InjectFields["source"]; !ok || v != "gateway" {
		t.Errorf("Transform.Request.Body.InjectFields[\"source\"] = %v, want %q", v, "gateway")
	}
	if len(txCfg.Request.Body.StripFields) != 1 || txCfg.Request.Body.StripFields[0] != "debug" {
		t.Errorf("Transform.Request.Body.StripFields = %v, want [debug]", txCfg.Request.Body.StripFields)
	}
	if v, ok := txCfg.Request.Body.RenameKeys["old_key"]; !ok || v != "new_key" {
		t.Errorf("Transform.Request.Body.RenameKeys[\"old_key\"] = %q, want %q", v, "new_key")
	}
	if txCfg.Response == nil {
		t.Fatal("transform.Response is nil, want non-nil")
	}
	if txCfg.Response.Headers == nil {
		t.Fatal("transform.Response.Headers is nil, want non-nil")
	}
	if v, ok := txCfg.Response.Headers.Add["X-Served-By"]; !ok || v != "api-gateway" {
		t.Errorf("Transform.Response.Headers.Add[\"X-Served-By\"] = %q, want %q", v, "api-gateway")
	}
	if len(txCfg.Response.Headers.Remove) != 1 || txCfg.Response.Headers.Remove[0] != "X-Debug" {
		t.Errorf("Transform.Response.Headers.Remove = %v, want [X-Debug]", txCfg.Response.Headers.Remove)
	}
	if v, ok := txCfg.Response.Headers.Rename["X-Backend-Id"]; !ok || v != "X-Request-Id" {
		t.Errorf("Transform.Response.Headers.Rename[\"X-Backend-Id\"] = %q, want %q", v, "X-Request-Id")
	}
	if txCfg.Response.Body == nil {
		t.Fatal("transform.Response.Body is nil, want non-nil")
	}
	if len(txCfg.Response.Body.StripFields) != 1 || txCfg.Response.Body.StripFields[0] != "internal_id" {
		t.Errorf("Transform.Response.Body.StripFields = %v, want [internal_id]", txCfg.Response.Body.StripFields)
	}

	// Route Retry
	if route.Retry == nil {
		t.Fatal("Route.Retry is nil, want non-nil")
	}
	if route.Retry.MaxRetries != 3 {
		t.Errorf("Route.Retry.MaxRetries = %d, want %d", route.Retry.MaxRetries, 3)
	}
	if route.Retry.InitialDelay != 200*time.Millisecond {
		t.Errorf("Route.Retry.InitialDelay = %v, want %v", route.Retry.InitialDelay, 200*time.Millisecond)
	}
	if route.Retry.Multiplier != 2.0 {
		t.Errorf("Route.Retry.Multiplier = %f, want %f", route.Retry.Multiplier, 2.0)
	}
	if len(route.Retry.RetryableStatusCodes) != 2 || route.Retry.RetryableStatusCodes[0] != 502 || route.Retry.RetryableStatusCodes[1] != 503 {
		t.Errorf("Route.Retry.RetryableStatusCodes = %v, want [502 503]", route.Retry.RetryableStatusCodes)
	}

	// Route CircuitBreaker
	if route.CircuitBreaker == nil {
		t.Fatal("Route.CircuitBreaker is nil, want non-nil")
	}
	if route.CircuitBreaker.FailureThreshold != 5 {
		t.Errorf("Route.CircuitBreaker.FailureThreshold = %d, want %d", route.CircuitBreaker.FailureThreshold, 5)
	}
	if route.CircuitBreaker.SuccessThreshold != 2 {
		t.Errorf("Route.CircuitBreaker.SuccessThreshold = %d, want %d", route.CircuitBreaker.SuccessThreshold, 2)
	}
	if route.CircuitBreaker.Timeout != 30*time.Second {
		t.Errorf("Route.CircuitBreaker.Timeout = %v, want %v", route.CircuitBreaker.Timeout, 30*time.Second)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	yamlContent := `routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "0.0.0.0")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, 8080)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want %v", cfg.Server.ReadTimeout, 30*time.Second)
	}
	if cfg.Server.WriteTimeout != 30*time.Second {
		t.Errorf("Server.WriteTimeout = %v, want %v", cfg.Server.WriteTimeout, 30*time.Second)
	}
	if cfg.Server.IdleTimeout != 120*time.Second {
		t.Errorf("Server.IdleTimeout = %v, want %v", cfg.Server.IdleTimeout, 120*time.Second)
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Errorf("Server.ShutdownTimeout = %v, want %v", cfg.Server.ShutdownTimeout, 15*time.Second)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "info")
	}
	if cfg.RateLimit.Backend != "memory" {
		t.Errorf("RateLimit.Backend = %q, want %q", cfg.RateLimit.Backend, "memory")
	}
}

func TestLoadConfig_EnvVarSubstitution(t *testing.T) {
	t.Setenv("TEST_SECRET", "my-secret")

	yamlContent := `
auth_providers:
  jwt_provider:
    type: "jwt"
    jwt:
      secret: "${TEST_SECRET}"
      algorithm: "HS256"
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	provider, ok := cfg.AuthProviders["jwt_provider"]
	if !ok {
		t.Fatal("AuthProviders[\"jwt_provider\"] not found")
	}
	if provider.JWT == nil {
		t.Fatal("JWT config is nil")
	}
	if provider.JWT.Secret != "my-secret" {
		t.Errorf("JWT.Secret = %q, want %q", provider.JWT.Secret, "my-secret")
	}
}

func TestLoadConfig_EnvVarNotSet_NonSensitiveField(t *testing.T) {
	// Unresolved env vars in non-sensitive fields are left as placeholders (no error).
	yamlContent := `
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://${UNSET_HOST_12345}:8080"
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Routes[0].Upstream.URL != "http://${UNSET_HOST_12345}:8080" {
		t.Errorf("Upstream.URL = %q, want placeholder preserved", cfg.Routes[0].Upstream.URL)
	}
}

func TestLoadConfig_EnvVarNotSet_SensitiveField_Errors(t *testing.T) {
	// Unresolved env vars in sensitive fields (jwt.secret) must produce a hard error.
	yamlContent := `
auth_providers:
  jwt_provider:
    type: "jwt"
    jwt:
      secret: "${UNSET_VAR_12345}"
      algorithm: "HS256"
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unresolved env var in jwt.secret, got nil")
	}
	if !strings.Contains(err.Error(), "unresolved env var") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_EnvVarSet_Substituted(t *testing.T) {
	const envKey = "TEST_ENVVAR_SET_12345"
	t.Setenv(envKey, "replaced-value")

	yamlContent := `
auth_providers:
  jwt_provider:
    type: "jwt"
    jwt:
      secret: "${TEST_ENVVAR_SET_12345}"
      algorithm: "HS256"
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	provider := cfg.AuthProviders["jwt_provider"]
	if provider.JWT.Secret != "replaced-value" {
		t.Errorf("JWT.Secret = %q, want %q", provider.JWT.Secret, "replaced-value")
	}
}

func TestLoadConfig_TransformVarsPreserved(t *testing.T) {
	yamlContent := `
middleware_definitions:
  my-transform:
    type: transform
    config:
      request:
        headers:
          add:
            X-Request-Start: "${timestamp_unix}"
      response:
        headers:
          add:
            X-Response-Time: "${latency_ms}ms"
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	def, ok := cfg.MiddlewareDefinitions["my-transform"]
	if !ok {
		t.Fatal("middleware definition 'my-transform' not found")
	}

	// The transform config is stored as a yaml.Node; marshal it back to check
	// that the ${...} placeholders survived config loading.
	raw, err := yaml.Marshal(&def.Config)
	if err != nil {
		t.Fatalf("failed to marshal transform config: %v", err)
	}
	content := string(raw)
	for _, placeholder := range []string{"${timestamp_unix}", "${latency_ms}"} {
		if !strings.Contains(content, placeholder) {
			t.Errorf("transform config should contain %q but got:\n%s", placeholder, content)
		}
	}
}

func TestLoadConfig_MixedEnvAndTransformVars(t *testing.T) {
	t.Setenv("REAL_ENV_VAR_67890", "env-value")

	yamlContent := `
auth_providers:
  jwt_provider:
    type: "jwt"
    jwt:
      secret: "${REAL_ENV_VAR_67890}"
      algorithm: "HS256"

middleware_definitions:
  my-transform:
    type: transform
    config:
      request:
        headers:
          add:
            X-Start: "${timestamp_unix}"
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	// Real env var should be substituted.
	provider := cfg.AuthProviders["jwt_provider"]
	if provider.JWT.Secret != "env-value" {
		t.Errorf("JWT.Secret = %q, want %q", provider.JWT.Secret, "env-value")
	}

	// Transform variable should be preserved.
	def := cfg.MiddlewareDefinitions["my-transform"]
	raw, _ := yaml.Marshal(&def.Config)
	if !strings.Contains(string(raw), "${timestamp_unix}") {
		t.Errorf("transform config should preserve ${timestamp_unix}, got:\n%s", string(raw))
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	yamlContent := `
server:
  host: "localhost"
  port: [invalid yaml
    ::::broken
`
	cfgPath := writeConfig(t, yamlContent)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("LoadConfig should return error for invalid YAML, got nil")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/to/config.yaml")
	if err == nil {
		t.Fatal("LoadConfig should return error for nonexistent file, got nil")
	}
}

func TestLoadConfig_DurationFields(t *testing.T) {
	yamlContent := `
server:
  read_timeout: 30s
  write_timeout: 1m
  idle_timeout: 100ms
  shutdown_timeout: 2m30s
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want %v", cfg.Server.ReadTimeout, 30*time.Second)
	}
	if cfg.Server.WriteTimeout != 1*time.Minute {
		t.Errorf("Server.WriteTimeout = %v, want %v", cfg.Server.WriteTimeout, 1*time.Minute)
	}
	if cfg.Server.IdleTimeout != 100*time.Millisecond {
		t.Errorf("Server.IdleTimeout = %v, want %v", cfg.Server.IdleTimeout, 100*time.Millisecond)
	}
	if cfg.Server.ShutdownTimeout != 2*time.Minute+30*time.Second {
		t.Errorf("Server.ShutdownTimeout = %v, want %v", cfg.Server.ShutdownTimeout, 2*time.Minute+30*time.Second)
	}
}

func TestLoadConfig_TrustedProxies(t *testing.T) {
	yamlContent := `
server:
  trusted_proxies:
    - "10.0.0.0/8"
    - "172.16.0.1"
routes: []
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if len(cfg.Server.TrustedProxies) != 2 {
		t.Fatalf("TrustedProxies length = %d, want 2", len(cfg.Server.TrustedProxies))
	}
	if cfg.Server.TrustedProxies[0] != "10.0.0.0/8" {
		t.Errorf("TrustedProxies[0] = %q, want %q", cfg.Server.TrustedProxies[0], "10.0.0.0/8")
	}
	if cfg.Server.TrustedProxies[1] != "172.16.0.1" {
		t.Errorf("TrustedProxies[1] = %q, want %q", cfg.Server.TrustedProxies[1], "172.16.0.1")
	}
}

func TestLoadConfig_MultipleEnvVarsInOneLine(t *testing.T) {
	t.Setenv("HOST", "localhost")
	t.Setenv("PORT", "3000")

	yamlContent := `
routes:
  - name: "test-route"
    match:
      path_regex: "^/test"
      methods:
        - "GET"
    upstream:
      url: "http://${HOST}:${PORT}"
`
	cfgPath := writeConfig(t, yamlContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if len(cfg.Routes) != 1 {
		t.Fatalf("Routes length = %d, want 1", len(cfg.Routes))
	}
	if cfg.Routes[0].Upstream.URL != "http://localhost:3000" {
		t.Errorf("Upstream.URL = %q, want %q", cfg.Routes[0].Upstream.URL, "http://localhost:3000")
	}
}

func TestValidate_NoneWithOtherProviders(t *testing.T) {
	yaml := `
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "s"
      algorithm: "HS256"
middleware_definitions:
  my-auth:
    type: auth
    config:
      providers: ["jwt_default", "none"]
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for none combined with other providers")
	}
	if !strings.Contains(err.Error(), "\"none\" cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_UndefinedProvider(t *testing.T) {
	yaml := `
auth_providers: {}
middleware_definitions:
  my-auth:
    type: auth
    config:
      providers: ["nonexistent"]
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for undefined provider")
	}
	if !strings.Contains(err.Error(), "not defined in auth_providers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_EmptyProviderName(t *testing.T) {
	yaml := `
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "s"
      algorithm: "HS256"
middleware_definitions:
  my-auth:
    type: auth
    config:
      providers: ["", "jwt_default"]
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for empty provider name")
	}
	if !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_EmptyProvidersSlice(t *testing.T) {
	yaml := `
auth_providers: {}
middleware_definitions:
  my-auth:
    type: auth
    config:
      providers: []
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for empty providers slice")
	}
	if !strings.Contains(err.Error(), "no providers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_DuplicateProviderName(t *testing.T) {
	yaml := `
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "s"
      algorithm: "HS256"
middleware_definitions:
  my-auth:
    type: auth
    config:
      providers: ["jwt_default", "jwt_default"]
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for duplicate provider name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RateLimit_InvalidRequests(t *testing.T) {
	for _, requests := range []int{0, -5} {
		yaml := fmt.Sprintf(`
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: %d
      window: 1m
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`, requests)
		cfgPath := writeConfig(t, yaml)
		_, err := LoadConfig(cfgPath)
		if err == nil {
			t.Fatalf("expected error for requests=%d", requests)
		}
		if !strings.Contains(err.Error(), "requests must be > 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestValidate_RateLimit_InvalidWindow(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 10
      window: 0s
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for zero window")
	}
	if !strings.Contains(err.Error(), "window must be > 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RateLimit_InvalidKeySource(t *testing.T) {
	for _, ks := range []string{"invalid", "header:"} {
		yaml := fmt.Sprintf(`
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 10
      window: 1m
      key_source: %q
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`, ks)
		cfgPath := writeConfig(t, yaml)
		_, err := LoadConfig(cfgPath)
		if err == nil {
			t.Fatalf("expected error for key_source=%q", ks)
		}
		if !strings.Contains(err.Error(), "invalid key_source") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestValidate_RateLimit_ValidConfig(t *testing.T) {
	for _, ks := range []string{"", "ip", "header:X-Tenant-ID"} {
		keySrc := ""
		if ks != "" {
			keySrc = fmt.Sprintf("      key_source: %q\n", ks)
		}
		yaml := fmt.Sprintf(`
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
%sroutes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`, keySrc)
		cfgPath := writeConfig(t, yaml)
		_, err := LoadConfig(cfgPath)
		if err != nil {
			t.Fatalf("unexpected error for key_source=%q: %v", ks, err)
		}
	}
}

func TestLoadConfig_RedisDefaults(t *testing.T) {
	yaml := `
rate_limit:
  backend: "redis"
  redis:
    addr: "localhost:6379"
routes: []
`
	cfgPath := writeConfig(t, yaml)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	r := cfg.RateLimit.Redis
	if r.PoolSize != 10 {
		t.Errorf("PoolSize = %d, want 10", r.PoolSize)
	}
	if r.DialTimeout != 5*time.Second {
		t.Errorf("DialTimeout = %v, want 5s", r.DialTimeout)
	}
	if r.ReadTimeout != 3*time.Second {
		t.Errorf("ReadTimeout = %v, want 3s", r.ReadTimeout)
	}
	if r.WriteTimeout != 3*time.Second {
		t.Errorf("WriteTimeout = %v, want 3s", r.WriteTimeout)
	}
	if r.QueryTimeout != 100*time.Millisecond {
		t.Errorf("QueryTimeout = %v, want 100ms", r.QueryTimeout)
	}
}

func TestLoadConfig_RedisDefaultsNotAppliedForMemoryBackend(t *testing.T) {
	yaml := `
rate_limit:
  backend: "memory"
  redis:
    addr: "localhost:6379"
routes: []
`
	cfgPath := writeConfig(t, yaml)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	r := cfg.RateLimit.Redis
	if r.PoolSize != 0 {
		t.Errorf("PoolSize = %d, want 0 (no defaults for memory backend)", r.PoolSize)
	}
	if r.DialTimeout != 0 {
		t.Errorf("DialTimeout = %v, want 0 (no defaults for memory backend)", r.DialTimeout)
	}
	if r.QueryTimeout != 0 {
		t.Errorf("QueryTimeout = %v, want 0 (no defaults for memory backend)", r.QueryTimeout)
	}
}

func TestValidate_RateLimit_InvalidAlgorithm(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 10
      window: 1m
      algorithm: "invalid"
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid algorithm")
	}
	if !strings.Contains(err.Error(), "algorithm") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RateLimit_ValidAlgorithms(t *testing.T) {
	for _, alg := range []string{"sliding_window", "leaky_bucket"} {
		yaml := fmt.Sprintf(`
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
      algorithm: %q
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`, alg)
		cfgPath := writeConfig(t, yaml)
		_, err := LoadConfig(cfgPath)
		if err != nil {
			t.Fatalf("unexpected error for algorithm=%q: %v", alg, err)
		}
	}
}

func TestLoadConfig_RateLimitAlgorithmDefault(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`
	cfgPath := writeConfig(t, yaml)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	// Resolve middleware to check the algorithm default.
	resolved, err := ResolveMiddlewares(cfg.Routes[0].Middlewares, cfg.MiddlewareDefinitions)
	if err != nil {
		t.Fatalf("ResolveMiddlewares error: %v", err)
	}
	rlCfg, ok := resolved[0].Config.(*RateLimitConfig)
	if !ok {
		t.Fatalf("resolved config is %T, want *RateLimitConfig", resolved[0].Config)
	}
	if rlCfg.Algorithm != "sliding_window" {
		t.Fatalf("expected default algorithm=sliding_window, got %q", rlCfg.Algorithm)
	}
}

func TestLoadConfig_PortZeroRejected(t *testing.T) {
	yamlContent := `
server:
  port: 0
routes: []
`
	cfgPath := writeConfig(t, yamlContent)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for port=0, got nil")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected error mentioning port, got: %v", err)
	}
}

func TestLoadConfig_UnknownFieldRejected(t *testing.T) {
	yamlContent := `
servr:
  port: 8080
routes: []
`
	cfgPath := writeConfig(t, yamlContent)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown field 'servr', got nil")
	}
	if !strings.Contains(err.Error(), "additional properties") {
		t.Fatalf("expected error mentioning additional properties, got: %v", err)
	}
}

// --- New middleware-specific validation tests ---

func TestValidate_UnknownMiddlewareRef(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: nonexistent
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown middleware ref")
	}
	if !strings.Contains(err.Error(), "not found in middleware_definitions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_UnknownMiddlewareType(t *testing.T) {
	yaml := `
middleware_definitions:
  bad:
    type: unknown_type
    config: {}
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: bad
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown middleware type")
	}
	// The error may come from JSON Schema validation (enum) or semantic validation.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "unknown middleware type") && !strings.Contains(errMsg, "validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_CORSNotFirstInChain(t *testing.T) {
	yaml := `
middleware_definitions:
  my-cors:
    type: cors
    config:
      allowed_origins: ["*"]
  my-auth:
    type: auth
    config:
      providers: ["none"]
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
      - ref: my-cors
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for CORS not first in chain")
	}
	if !strings.Contains(err.Error(), "CORS middleware") && !strings.Contains(err.Error(), "must be first") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_CORSFirstInChain_OK(t *testing.T) {
	yaml := `
middleware_definitions:
  my-cors:
    type: cors
    config:
      allowed_origins: ["*"]
  my-auth:
    type: auth
    config:
      providers: ["none"]
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-cors
      - ref: my-auth
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("expected no error for CORS first in chain, got: %v", err)
	}
}

func TestValidate_SkipMiddlewares_UnknownName(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
middlewares:
  - ref: rl
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    skip_middlewares:
      - "nonexistent"
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for skip_middlewares with unknown name")
	}
	if !strings.Contains(err.Error(), "skip_middlewares") && !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_SkipMiddlewares_ValidName(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
middlewares:
  - ref: rl
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    skip_middlewares:
      - "rl"
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("expected no error for valid skip_middlewares, got: %v", err)
	}
}

func TestValidate_GlobalMiddlewares(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
middlewares:
  - ref: rl
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
`
	cfgPath := writeConfig(t, yaml)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Middlewares) != 1 {
		t.Fatalf("global middlewares length = %d, want 1", len(cfg.Middlewares))
	}
	if cfg.Middlewares[0].Ref != "rl" {
		t.Errorf("global middleware ref = %q, want %q", cfg.Middlewares[0].Ref, "rl")
	}
}

func TestValidate_GlobalMiddlewares_UnknownRef(t *testing.T) {
	yaml := `
middleware_definitions: {}
middlewares:
  - ref: nonexistent
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown global middleware ref")
	}
	if !strings.Contains(err.Error(), "not found in middleware_definitions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_ClaimKeySourceWithoutPrecedingAuth(t *testing.T) {
	yaml := `
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "s"
      algorithm: "HS256"
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
      key_source: "claim:user_id"
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for claim: key_source without preceding auth")
	}
	if !strings.Contains(err.Error(), "no auth middleware precedes it") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_ClaimKeySourceWithPrecedingAuth(t *testing.T) {
	yaml := `
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "s"
      algorithm: "HS256"
middleware_definitions:
  my-auth:
    type: auth
    config:
      providers: ["jwt_default"]
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
      key_source: "claim:user_id"
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: my-auth
      - ref: rl
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("expected no error with auth before claim key_source, got: %v", err)
	}
}

func TestValidate_MiddlewareRefWithOverride(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
      key_source: "ip"
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    middlewares:
      - ref: rl
        config:
          requests: 200
`
	cfgPath := writeConfig(t, yaml)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resolved, err := ResolveMiddlewares(cfg.Routes[0].Middlewares, cfg.MiddlewareDefinitions)
	if err != nil {
		t.Fatalf("ResolveMiddlewares error: %v", err)
	}
	rlCfg, ok := resolved[0].Config.(*RateLimitConfig)
	if !ok {
		t.Fatalf("resolved config is %T, want *RateLimitConfig", resolved[0].Config)
	}
	if rlCfg.Requests != 200 {
		t.Errorf("overridden requests = %d, want 200", rlCfg.Requests)
	}
	// The non-overridden field should be preserved from the base definition.
	if rlCfg.Window != time.Minute {
		t.Errorf("preserved window = %v, want 1m", rlCfg.Window)
	}
	if rlCfg.KeySource != "ip" {
		t.Errorf("preserved key_source = %q, want %q", rlCfg.KeySource, "ip")
	}
}

func TestValidateResolvedMiddlewares_TypeAssertionFailure(t *testing.T) {
	tests := []struct {
		name      string
		mw        ResolvedMiddleware
		wantError string
	}{
		{
			name: "rate_limit with wrong config type",
			mw: ResolvedMiddleware{
				Name:   "bad-rl",
				Type:   "rate_limit",
				Config: &AuthMiddlewareConfig{Providers: []string{"none"}},
			},
			wantError: "internal error: expected *RateLimitConfig",
		},
		{
			name: "auth with wrong config type",
			mw: ResolvedMiddleware{
				Name:   "bad-auth",
				Type:   "auth",
				Config: &RateLimitConfig{Requests: 10},
			},
			wantError: "internal error: expected *AuthMiddlewareConfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateResolvedMiddlewares("test", []ResolvedMiddleware{tt.mw}, &GatewayConfig{})
			if len(errs) == 0 {
				t.Fatal("expected error for type assertion failure, got none")
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e, tt.wantError) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected error containing %q, got: %v", tt.wantError, errs)
			}
		})
	}
}

func TestSemanticErrors_GlobalResolutionFailure(t *testing.T) {
	yaml := `
middleware_definitions:
  rl:
    type: rate_limit
    config:
      requests: 100
      window: 1m
middlewares:
  - ref: nonexistent-global
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
`
	cfgPath := writeConfig(t, yaml)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for nonexistent global middleware ref")
	}
	if !strings.Contains(err.Error(), "nonexistent-global") {
		t.Fatalf("expected error mentioning nonexistent-global, got: %v", err)
	}
}

func TestLoadConfig_ValidNewFormat_Success(t *testing.T) {
	yaml := `
auth_providers:
  jwt_default:
    type: "jwt"
    jwt:
      secret: "s"
      algorithm: "HS256"
middleware_definitions:
  my-cors:
    type: cors
    config:
      allowed_origins: ["*"]
  my-auth:
    type: auth
    config:
      providers: ["jwt_default"]
  ip-limiter:
    type: rate_limit
    config:
      requests: 100
      window: 1m
      key_source: "ip"
  my-transform:
    type: transform
    config:
      request:
        headers:
          add:
            X-Gateway: "tanugate"
middlewares:
  - ref: ip-limiter
routes:
  - name: "api"
    match:
      path_regex: "^/api"
    upstream:
      url: "http://svc:8080"
    skip_middlewares:
      - "ip-limiter"
    middlewares:
      - ref: my-cors
      - ref: my-auth
      - ref: ip-limiter
        config:
          requests: 200
      - ref: my-transform
`
	cfgPath := writeConfig(t, yaml)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error loading valid new-format config: %v", err)
	}

	if len(cfg.MiddlewareDefinitions) != 4 {
		t.Errorf("MiddlewareDefinitions length = %d, want 4", len(cfg.MiddlewareDefinitions))
	}
	if len(cfg.Middlewares) != 1 {
		t.Errorf("global Middlewares length = %d, want 1", len(cfg.Middlewares))
	}
	if len(cfg.Routes[0].Middlewares) != 4 {
		t.Errorf("route Middlewares length = %d, want 4", len(cfg.Routes[0].Middlewares))
	}
	if len(cfg.Routes[0].SkipMiddlewares) != 1 {
		t.Errorf("route SkipMiddlewares length = %d, want 1", len(cfg.Routes[0].SkipMiddlewares))
	}
}

func TestLoadConfig_DefaultsApplied_MaxHeaderBytes(t *testing.T) {
	cfgPath := writeConfig(t, `routes: []`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Server.MaxHeaderBytes != 1<<20 {
		t.Errorf("Server.MaxHeaderBytes = %d, want %d", cfg.Server.MaxHeaderBytes, 1<<20)
	}
}

func TestLoadConfig_MaxHeaderBytes_ExplicitValue(t *testing.T) {
	cfgPath := writeConfig(t, `
server:
  max_header_bytes: 8192
routes: []
`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Server.MaxHeaderBytes != 8192 {
		t.Errorf("Server.MaxHeaderBytes = %d, want %d", cfg.Server.MaxHeaderBytes, 8192)
	}
}

func TestSemanticErrors_UnresolvedEnvVar_JWTSecret(t *testing.T) {
	cfgPath := writeConfig(t, `
auth_providers:
  myjwt:
    type: "jwt"
    jwt:
      secret: "${JWT_SECRET_UNSET}"
      algorithm: "HS256"
routes: []
`)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unresolved env var in jwt.secret")
	}
	if !strings.Contains(err.Error(), "auth_providers.myjwt.jwt.secret") || !strings.Contains(err.Error(), "unresolved env var") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSemanticErrors_UnresolvedEnvVar_APIKeyKey(t *testing.T) {
	cfgPath := writeConfig(t, `
auth_providers:
  mykey:
    type: "apikey"
    api_key:
      header: "X-API-Key"
      keys:
        - key: "${API_KEY_UNSET}"
          name: "svc1"
routes: []
`)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unresolved env var in api_key.keys[0].key")
	}
	if !strings.Contains(err.Error(), "auth_providers.mykey.api_key.keys[0].key") || !strings.Contains(err.Error(), "unresolved env var") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSemanticErrors_UnresolvedEnvVar_OIDCClientSecret(t *testing.T) {
	cfgPath := writeConfig(t, `
auth_providers:
  myoidc:
    type: "oidc"
    oidc:
      issuer_url: "https://auth.example.com"
      client_id: "gateway"
      client_secret: "${OIDC_SECRET_UNSET}"
routes: []
`)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unresolved env var in oidc.client_secret")
	}
	if !strings.Contains(err.Error(), "auth_providers.myoidc.oidc.client_secret") || !strings.Contains(err.Error(), "unresolved env var") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSemanticErrors_UnresolvedEnvVar_RedisPassword(t *testing.T) {
	cfgPath := writeConfig(t, `
rate_limit:
  backend: "redis"
  redis:
    addr: "localhost:6379"
    password: "${REDIS_PASS_UNSET}"
routes: []
`)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for unresolved env var in redis.password")
	}
	if !strings.Contains(err.Error(), "rate_limit.redis.password") || !strings.Contains(err.Error(), "unresolved env var") {
		t.Errorf("unexpected error: %v", err)
	}
}
