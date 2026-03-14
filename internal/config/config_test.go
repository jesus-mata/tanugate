package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

cors:
  allowed_origins:
    - "https://example.com"
    - "https://other.com"
  allowed_methods:
    - "GET"
    - "POST"
    - "PUT"
  allowed_headers:
    - "Authorization"
    - "Content-Type"
  allow_credentials: true
  max_age: 3600

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
    auth:
      providers:
        - "main_jwt"
    rate_limit:
      requests_per_window: 100
      window: 1m
      key_source: "ip"
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
    transform:
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
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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

	// CORS
	if len(cfg.CORS.AllowedOrigins) != 2 {
		t.Fatalf("CORS.AllowedOrigins length = %d, want 2", len(cfg.CORS.AllowedOrigins))
	}
	if cfg.CORS.AllowedOrigins[0] != "https://example.com" {
		t.Errorf("CORS.AllowedOrigins[0] = %q, want %q", cfg.CORS.AllowedOrigins[0], "https://example.com")
	}
	if cfg.CORS.AllowedOrigins[1] != "https://other.com" {
		t.Errorf("CORS.AllowedOrigins[1] = %q, want %q", cfg.CORS.AllowedOrigins[1], "https://other.com")
	}
	if len(cfg.CORS.AllowedMethods) != 3 {
		t.Fatalf("CORS.AllowedMethods length = %d, want 3", len(cfg.CORS.AllowedMethods))
	}
	if cfg.CORS.AllowedMethods[0] != "GET" || cfg.CORS.AllowedMethods[1] != "POST" || cfg.CORS.AllowedMethods[2] != "PUT" {
		t.Errorf("CORS.AllowedMethods = %v, want [GET POST PUT]", cfg.CORS.AllowedMethods)
	}
	if len(cfg.CORS.AllowedHeaders) != 2 {
		t.Fatalf("CORS.AllowedHeaders length = %d, want 2", len(cfg.CORS.AllowedHeaders))
	}
	if cfg.CORS.AllowedHeaders[0] != "Authorization" || cfg.CORS.AllowedHeaders[1] != "Content-Type" {
		t.Errorf("CORS.AllowedHeaders = %v, want [Authorization Content-Type]", cfg.CORS.AllowedHeaders)
	}
	if !cfg.CORS.AllowCredentials {
		t.Errorf("CORS.AllowCredentials = false, want true")
	}
	if cfg.CORS.MaxAge != 3600 {
		t.Errorf("CORS.MaxAge = %d, want %d", cfg.CORS.MaxAge, 3600)
	}

	// RateLimit
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

	// Route Auth
	if route.Auth == nil {
		t.Fatal("Route.Auth is nil, want non-nil")
	}
	if len(route.Auth.Providers) != 1 || route.Auth.Providers[0] != "main_jwt" {
		t.Errorf("Route.Auth.Providers = %v, want [main_jwt]", route.Auth.Providers)
	}

	// Route RateLimit
	if route.RateLimit == nil {
		t.Fatal("Route.RateLimit is nil, want non-nil")
	}
	if route.RateLimit.RequestsPerWindow != 100 {
		t.Errorf("Route.RateLimit.RequestsPerWindow = %d, want %d", route.RateLimit.RequestsPerWindow, 100)
	}
	if route.RateLimit.Window != 1*time.Minute {
		t.Errorf("Route.RateLimit.Window = %v, want %v", route.RateLimit.Window, 1*time.Minute)
	}
	if route.RateLimit.KeySource != "ip" {
		t.Errorf("Route.RateLimit.KeySource = %q, want %q", route.RateLimit.KeySource, "ip")
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

	// Route Transform - Request
	if route.Transform == nil {
		t.Fatal("Route.Transform is nil, want non-nil")
	}
	if route.Transform.Request == nil {
		t.Fatal("Route.Transform.Request is nil, want non-nil")
	}
	if route.Transform.Request.Headers == nil {
		t.Fatal("Route.Transform.Request.Headers is nil, want non-nil")
	}
	if v, ok := route.Transform.Request.Headers.Add["X-Gateway"]; !ok || v != "true" {
		t.Errorf("Transform.Request.Headers.Add[\"X-Gateway\"] = %q, want %q", v, "true")
	}
	if len(route.Transform.Request.Headers.Remove) != 1 || route.Transform.Request.Headers.Remove[0] != "X-Internal" {
		t.Errorf("Transform.Request.Headers.Remove = %v, want [X-Internal]", route.Transform.Request.Headers.Remove)
	}
	if v, ok := route.Transform.Request.Headers.Rename["X-Old"]; !ok || v != "X-New" {
		t.Errorf("Transform.Request.Headers.Rename[\"X-Old\"] = %q, want %q", v, "X-New")
	}
	if route.Transform.Request.Body == nil {
		t.Fatal("Route.Transform.Request.Body is nil, want non-nil")
	}
	if v, ok := route.Transform.Request.Body.InjectFields["source"]; !ok || v != "gateway" {
		t.Errorf("Transform.Request.Body.InjectFields[\"source\"] = %v, want %q", v, "gateway")
	}
	if len(route.Transform.Request.Body.StripFields) != 1 || route.Transform.Request.Body.StripFields[0] != "debug" {
		t.Errorf("Transform.Request.Body.StripFields = %v, want [debug]", route.Transform.Request.Body.StripFields)
	}
	if v, ok := route.Transform.Request.Body.RenameKeys["old_key"]; !ok || v != "new_key" {
		t.Errorf("Transform.Request.Body.RenameKeys[\"old_key\"] = %q, want %q", v, "new_key")
	}

	// Route Transform - Response
	if route.Transform.Response == nil {
		t.Fatal("Route.Transform.Response is nil, want non-nil")
	}
	if route.Transform.Response.Headers == nil {
		t.Fatal("Route.Transform.Response.Headers is nil, want non-nil")
	}
	if v, ok := route.Transform.Response.Headers.Add["X-Served-By"]; !ok || v != "api-gateway" {
		t.Errorf("Transform.Response.Headers.Add[\"X-Served-By\"] = %q, want %q", v, "api-gateway")
	}
	if len(route.Transform.Response.Headers.Remove) != 1 || route.Transform.Response.Headers.Remove[0] != "X-Debug" {
		t.Errorf("Transform.Response.Headers.Remove = %v, want [X-Debug]", route.Transform.Response.Headers.Remove)
	}
	if v, ok := route.Transform.Response.Headers.Rename["X-Backend-Id"]; !ok || v != "X-Request-Id" {
		t.Errorf("Transform.Response.Headers.Rename[\"X-Backend-Id\"] = %q, want %q", v, "X-Request-Id")
	}
	if route.Transform.Response.Body == nil {
		t.Fatal("Route.Transform.Response.Body is nil, want non-nil")
	}
	if len(route.Transform.Response.Body.StripFields) != 1 || route.Transform.Response.Body.StripFields[0] != "internal_id" {
		t.Errorf("Transform.Response.Body.StripFields = %v, want [internal_id]", route.Transform.Response.Body.StripFields)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	yamlContent := `routes: []
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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

func TestLoadConfig_EnvVarNotSet(t *testing.T) {
	yamlContent := `
auth_providers:
  jwt_provider:
    type: "jwt"
    jwt:
      secret: "${UNSET_VAR_12345}"
      algorithm: "HS256"
routes: []
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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
	if provider.JWT.Secret != "" {
		t.Errorf("JWT.Secret = %q, want empty string", provider.JWT.Secret)
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	yamlContent := `
server:
  host: "localhost"
  port: [invalid yaml
    ::::broken
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    auth:
      providers: ["jwt_default", "none"]
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    auth:
      providers: ["nonexistent"]
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    auth:
      providers: ["", "jwt_default"]
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    auth:
      providers: []
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for empty providers slice")
	}
	if !strings.Contains(err.Error(), "no providers are configured") {
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    auth:
      providers: ["jwt_default", "jwt_default"]
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for duplicate provider name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RateLimit_InvalidRequestsPerWindow(t *testing.T) {
	for _, rpw := range []int{0, -5} {
		yaml := fmt.Sprintf(`
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: %d
      window: 1m
`, rpw)
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadConfig(cfgPath)
		if err == nil {
			t.Fatalf("expected error for requests_per_window=%d", rpw)
		}
		if !strings.Contains(err.Error(), "requests_per_window must be > 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestValidate_RateLimit_InvalidWindow(t *testing.T) {
	yaml := `
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: 10
      window: 0s
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: 10
      window: 1m
      key_source: %q
`, ks)
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: 100
      window: 1m
%s`, keySrc)
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
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
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: 10
      window: 1m
      algorithm: "invalid"
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid algorithm")
	}
	if !strings.Contains(err.Error(), "invalid algorithm") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RateLimit_ValidAlgorithms(t *testing.T) {
	for _, alg := range []string{"sliding_window", "leaky_bucket"} {
		yaml := fmt.Sprintf(`
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: 100
      window: 1m
      algorithm: %q
`, alg)
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadConfig(cfgPath)
		if err != nil {
			t.Fatalf("unexpected error for algorithm=%q: %v", alg, err)
		}
	}
}

func TestLoadConfig_RateLimitAlgorithmDefault(t *testing.T) {
	yaml := `
routes:
  - name: "test"
    match:
      path_regex: "^/test"
    upstream:
      url: "http://localhost:8080"
    rate_limit:
      requests_per_window: 100
      window: 1m
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Routes[0].RateLimit.Algorithm != "sliding_window" {
		t.Fatalf("expected default algorithm=sliding_window, got %q", cfg.Routes[0].RateLimit.Algorithm)
	}
}
