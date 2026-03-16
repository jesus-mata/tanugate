package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlNode is a test helper that parses a YAML string into a yaml.Node.
func yamlNode(t *testing.T, s string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("yamlNode: unmarshal error: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return *doc.Content[0]
	}
	return doc
}

func TestResolveMiddlewares_Basic(t *testing.T) {
	defs := map[string]MiddlewareDefinition{
		"my-auth": {
			Type:   "auth",
			Config: yamlNode(t, "providers: [none]"),
		},
	}
	refs := []MiddlewareRef{
		{Ref: "my-auth"},
	}

	resolved, err := ResolveMiddlewares(refs, defs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved length = %d, want 1", len(resolved))
	}
	if resolved[0].Name != "my-auth" {
		t.Errorf("Name = %q, want %q", resolved[0].Name, "my-auth")
	}
	if resolved[0].Type != "auth" {
		t.Errorf("Type = %q, want %q", resolved[0].Type, "auth")
	}
	authCfg, ok := resolved[0].Config.(*AuthMiddlewareConfig)
	if !ok {
		t.Fatalf("Config is %T, want *AuthMiddlewareConfig", resolved[0].Config)
	}
	if len(authCfg.Providers) != 1 || authCfg.Providers[0] != "none" {
		t.Errorf("Providers = %v, want [none]", authCfg.Providers)
	}
}

func TestResolveMiddlewares_NotFound(t *testing.T) {
	defs := map[string]MiddlewareDefinition{}
	refs := []MiddlewareRef{
		{Ref: "nonexistent"},
	}

	_, err := ResolveMiddlewares(refs, defs)
	if err == nil {
		t.Fatal("expected error for unknown ref")
	}
	if !strings.Contains(err.Error(), "not found in middleware_definitions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveMiddlewares_UnknownType(t *testing.T) {
	defs := map[string]MiddlewareDefinition{
		"bad": {
			Type:   "unknown_type",
			Config: yamlNode(t, "key: value"),
		},
	}
	refs := []MiddlewareRef{
		{Ref: "bad"},
	}

	_, err := ResolveMiddlewares(refs, defs)
	if err == nil {
		t.Fatal("expected error for unknown middleware type")
	}
	if !strings.Contains(err.Error(), "unknown middleware type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveMiddlewares_Override(t *testing.T) {
	defs := map[string]MiddlewareDefinition{
		"rl": {
			Type:   "rate_limit",
			Config: yamlNode(t, "requests: 100\nwindow: 60s\nkey_source: ip"),
		},
	}
	refs := []MiddlewareRef{
		{
			Ref:    "rl",
			Config: yamlNode(t, "requests: 200"),
		},
	}

	resolved, err := ResolveMiddlewares(refs, defs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rlCfg, ok := resolved[0].Config.(*RateLimitConfig)
	if !ok {
		t.Fatalf("Config is %T, want *RateLimitConfig", resolved[0].Config)
	}
	if rlCfg.Requests != 200 {
		t.Errorf("Requests = %d, want 200 (overridden)", rlCfg.Requests)
	}
	if rlCfg.Window != 60*time.Second {
		t.Errorf("Window = %v, want 60s (preserved from base)", rlCfg.Window)
	}
	if rlCfg.KeySource != "ip" {
		t.Errorf("KeySource = %q, want %q (preserved from base)", rlCfg.KeySource, "ip")
	}
}

func TestResolveMiddlewares_MultipleRefs(t *testing.T) {
	defs := map[string]MiddlewareDefinition{
		"cors": {
			Type:   "cors",
			Config: yamlNode(t, "allowed_origins: [\"*\"]"),
		},
		"auth": {
			Type:   "auth",
			Config: yamlNode(t, "providers: [none]"),
		},
		"rl": {
			Type:   "rate_limit",
			Config: yamlNode(t, "requests: 50\nwindow: 30s"),
		},
	}
	refs := []MiddlewareRef{
		{Ref: "cors"},
		{Ref: "auth"},
		{Ref: "rl"},
	}

	resolved, err := ResolveMiddlewares(refs, defs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 3 {
		t.Fatalf("resolved length = %d, want 3", len(resolved))
	}
	if resolved[0].Type != "cors" {
		t.Errorf("resolved[0].Type = %q, want %q", resolved[0].Type, "cors")
	}
	if resolved[1].Type != "auth" {
		t.Errorf("resolved[1].Type = %q, want %q", resolved[1].Type, "auth")
	}
	if resolved[2].Type != "rate_limit" {
		t.Errorf("resolved[2].Type = %q, want %q", resolved[2].Type, "rate_limit")
	}
}

func TestResolveMiddlewares_EmptyRefs(t *testing.T) {
	defs := map[string]MiddlewareDefinition{
		"rl": {Type: "rate_limit", Config: yamlNode(t, "requests: 100\nwindow: 60s")},
	}

	resolved, err := ResolveMiddlewares(nil, defs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved length = %d, want 0", len(resolved))
	}
}

func TestMergeYAMLNodes_ShallowMerge(t *testing.T) {
	base := yamlNode(t, "requests: 100\nwindow: 60s\nkey_source: ip")
	override := yamlNode(t, "requests: 200")

	merged, err := mergeYAMLNodes(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg RateLimitConfig
	if err := merged.Decode(&cfg); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if cfg.Requests != 200 {
		t.Errorf("Requests = %d, want 200 (overridden)", cfg.Requests)
	}
	if cfg.Window != 60*time.Second {
		t.Errorf("Window = %v, want 60s (preserved from base)", cfg.Window)
	}
	if cfg.KeySource != "ip" {
		t.Errorf("KeySource = %q, want %q (preserved from base)", cfg.KeySource, "ip")
	}
}

func TestMergeYAMLNodes_OverrideAddsNewKey(t *testing.T) {
	base := yamlNode(t, "requests: 100\nwindow: 60s")
	override := yamlNode(t, "key_source: ip")

	merged, err := mergeYAMLNodes(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg RateLimitConfig
	if err := merged.Decode(&cfg); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if cfg.Requests != 100 {
		t.Errorf("Requests = %d, want 100", cfg.Requests)
	}
	if cfg.KeySource != "ip" {
		t.Errorf("KeySource = %q, want %q (added from override)", cfg.KeySource, "ip")
	}
}

func TestMergeYAMLNodes_EmptyBase(t *testing.T) {
	base := yaml.Node{} // zero-value (Kind == 0)
	override := yamlNode(t, "requests: 200\nwindow: 30s")

	merged, err := mergeYAMLNodes(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg RateLimitConfig
	if err := merged.Decode(&cfg); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if cfg.Requests != 200 {
		t.Errorf("Requests = %d, want 200", cfg.Requests)
	}
}

func TestMergeYAMLNodes_NonMappingBase(t *testing.T) {
	base := yamlNode(t, "- item1\n- item2") // sequence node
	override := yamlNode(t, "key: value")

	_, err := mergeYAMLNodes(base, override)
	if err == nil {
		t.Fatal("expected error for non-mapping base node")
	}
	if !strings.Contains(err.Error(), "not a mapping") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeYAMLNodes_NonMappingOverride(t *testing.T) {
	base := yamlNode(t, "key: value")
	override := yamlNode(t, "- item1\n- item2") // sequence node

	_, err := mergeYAMLNodes(base, override)
	if err == nil {
		t.Fatal("expected error for non-mapping override node")
	}
	if !strings.Contains(err.Error(), "not a mapping") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMergeYAMLNodes_NestedMappingFullyReplaced(t *testing.T) {
	base := yamlNode(t, `
request:
  headers:
    add:
      X-Gateway-Route: "user-service"
  body:
    inject_fields:
      _gateway_timestamp: "2024-01-01T00:00:00Z"
`)
	override := yamlNode(t, `
request:
  headers:
    add:
      X-Override: "true"
`)

	merged, err := mergeYAMLNodes(base, override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg TransformConfig
	if err := merged.Decode(&cfg); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	// The entire "request" subtree from base is replaced by the override's
	// "request" subtree. Since the override's request only contains headers,
	// the body key from base is lost — this is the shallow merge behaviour.
	if cfg.Request == nil {
		t.Fatal("Request is nil, expected non-nil from override")
	}
	if cfg.Request.Body != nil {
		t.Errorf("Request.Body = %+v, want nil (shallow merge should drop base body)", cfg.Request.Body)
	}
	if cfg.Request.Headers == nil {
		t.Fatal("Request.Headers is nil, expected non-nil from override")
	}
	if v, ok := cfg.Request.Headers.Add["X-Override"]; !ok || v != "true" {
		t.Errorf("Request.Headers.Add[\"X-Override\"] = %q, want %q", v, "true")
	}
	if _, ok := cfg.Request.Headers.Add["X-Gateway-Route"]; ok {
		t.Error("Request.Headers.Add contains base header \"X-Gateway-Route\", expected only override headers")
	}
}

func TestDecodeTypedConfig_CORS(t *testing.T) {
	node := yamlNode(t, `
allowed_origins: ["https://example.com"]
allowed_methods: ["GET", "POST"]
allow_credentials: true
max_age: 3600
`)

	cfg, err := decodeTypedConfig("cors", node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	corsCfg, ok := cfg.(*CORSConfig)
	if !ok {
		t.Fatalf("config is %T, want *CORSConfig", cfg)
	}
	if len(corsCfg.AllowedOrigins) != 1 || corsCfg.AllowedOrigins[0] != "https://example.com" {
		t.Errorf("AllowedOrigins = %v, want [https://example.com]", corsCfg.AllowedOrigins)
	}
	if !corsCfg.AllowCredentials {
		t.Error("AllowCredentials = false, want true")
	}
	if corsCfg.MaxAge != 3600 {
		t.Errorf("MaxAge = %d, want 3600", corsCfg.MaxAge)
	}
}

func TestDecodeTypedConfig_RateLimit(t *testing.T) {
	node := yamlNode(t, `
requests: 50
window: 30s
key_source: "header:X-Tenant"
`)

	cfg, err := decodeTypedConfig("rate_limit", node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rlCfg, ok := cfg.(*RateLimitConfig)
	if !ok {
		t.Fatalf("config is %T, want *RateLimitConfig", cfg)
	}
	if rlCfg.Requests != 50 {
		t.Errorf("Requests = %d, want 50", rlCfg.Requests)
	}
	if rlCfg.Window != 30*time.Second {
		t.Errorf("Window = %v, want 30s", rlCfg.Window)
	}
	if rlCfg.KeySource != "header:X-Tenant" {
		t.Errorf("KeySource = %q, want %q", rlCfg.KeySource, "header:X-Tenant")
	}
	// Verify algorithm default is applied.
	if rlCfg.Algorithm != "sliding_window" {
		t.Errorf("Algorithm = %q, want %q (default)", rlCfg.Algorithm, "sliding_window")
	}
}

func TestDecodeTypedConfig_RateLimit_ExplicitAlgorithm(t *testing.T) {
	node := yamlNode(t, `
requests: 50
window: 30s
algorithm: leaky_bucket
`)

	cfg, err := decodeTypedConfig("rate_limit", node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rlCfg := cfg.(*RateLimitConfig)
	if rlCfg.Algorithm != "leaky_bucket" {
		t.Errorf("Algorithm = %q, want %q", rlCfg.Algorithm, "leaky_bucket")
	}
}

func TestDecodeTypedConfig_Auth(t *testing.T) {
	node := yamlNode(t, `
providers: ["jwt_default", "api_key"]
`)

	cfg, err := decodeTypedConfig("auth", node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	authCfg, ok := cfg.(*AuthMiddlewareConfig)
	if !ok {
		t.Fatalf("config is %T, want *AuthMiddlewareConfig", cfg)
	}
	if len(authCfg.Providers) != 2 {
		t.Fatalf("Providers length = %d, want 2", len(authCfg.Providers))
	}
	if authCfg.Providers[0] != "jwt_default" || authCfg.Providers[1] != "api_key" {
		t.Errorf("Providers = %v, want [jwt_default api_key]", authCfg.Providers)
	}
}

func TestDecodeTypedConfig_Transform(t *testing.T) {
	node := yamlNode(t, `
request:
  headers:
    add:
      X-Gateway: "tanugate"
    remove:
      - "X-Debug"
response:
  headers:
    add:
      X-Served-By: "gateway"
`)

	cfg, err := decodeTypedConfig("transform", node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	txCfg, ok := cfg.(*TransformConfig)
	if !ok {
		t.Fatalf("config is %T, want *TransformConfig", cfg)
	}
	if txCfg.Request == nil {
		t.Fatal("Request is nil")
	}
	if txCfg.Request.Headers == nil {
		t.Fatal("Request.Headers is nil")
	}
	if v, ok := txCfg.Request.Headers.Add["X-Gateway"]; !ok || v != "tanugate" {
		t.Errorf("Request.Headers.Add[\"X-Gateway\"] = %q, want %q", v, "tanugate")
	}
	if len(txCfg.Request.Headers.Remove) != 1 || txCfg.Request.Headers.Remove[0] != "X-Debug" {
		t.Errorf("Request.Headers.Remove = %v, want [X-Debug]", txCfg.Request.Headers.Remove)
	}
	if txCfg.Response == nil {
		t.Fatal("Response is nil")
	}
	if v, ok := txCfg.Response.Headers.Add["X-Served-By"]; !ok || v != "gateway" {
		t.Errorf("Response.Headers.Add[\"X-Served-By\"] = %q, want %q", v, "gateway")
	}
}

func TestDecodeTypedConfig_UnknownType(t *testing.T) {
	node := yamlNode(t, "key: value")
	_, err := decodeTypedConfig("bogus", node)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	if !strings.Contains(err.Error(), "unknown middleware type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeTypedConfig_EmptyNode(t *testing.T) {
	// A zero-value yaml.Node (Kind == 0) should produce defaults.
	node := yaml.Node{}

	cfg, err := decodeTypedConfig("rate_limit", node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rlCfg, ok := cfg.(*RateLimitConfig)
	if !ok {
		t.Fatalf("config is %T, want *RateLimitConfig", cfg)
	}
	// Should have the default algorithm applied.
	if rlCfg.Algorithm != "sliding_window" {
		t.Errorf("Algorithm = %q, want %q (default)", rlCfg.Algorithm, "sliding_window")
	}
	// Other fields should be zero values.
	if rlCfg.Requests != 0 {
		t.Errorf("Requests = %d, want 0", rlCfg.Requests)
	}
}
