package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/invopop/jsonschema"
)

func TestGenerateSchema_TopLevelProperties(t *testing.T) {
	schema := GenerateSchema()
	if schema.Properties == nil {
		t.Fatal("schema.Properties is nil, expected top-level properties")
	}

	expected := []string{"server", "logging", "cors", "rate_limit", "auth_providers", "routes"}
	for _, name := range expected {
		_, ok := schema.Properties.Get(name)
		if !ok {
			t.Errorf("schema is missing top-level property %q", name)
		}
	}
}

func TestGenerateSchema_DraftVersion(t *testing.T) {
	schema := GenerateSchema()
	if schema.Version == "" {
		t.Fatal("schema.$schema is empty, expected draft version to be set")
	}
	if !strings.Contains(schema.Version, "2020-12") {
		t.Errorf("schema.$schema = %q, expected it to contain \"2020-12\"", schema.Version)
	}
}

func TestGenerateSchema_DurationFields(t *testing.T) {
	schema := GenerateSchema()
	if schema.Properties == nil {
		t.Fatal("schema.Properties is nil")
	}

	serverSchema, ok := schema.Properties.Get("server")
	if !ok {
		t.Fatal("schema missing \"server\" property")
	}

	// ServerConfig may be a $ref; resolve it from $defs if needed.
	resolved := resolveRef(t, schema, serverSchema)
	if resolved.Properties == nil {
		t.Fatal("server schema has no properties")
	}

	readTimeout, ok := resolved.Properties.Get("read_timeout")
	if !ok {
		t.Fatal("server schema missing \"read_timeout\" property")
	}

	readTimeoutResolved := resolveRef(t, schema, readTimeout)
	if readTimeoutResolved.Type != "string" {
		t.Errorf("read_timeout type = %q, want \"string\" (duration fields should be strings)", readTimeoutResolved.Type)
	}
}

func TestGenerateSchema_EnumValues(t *testing.T) {
	schema := GenerateSchema()

	// Find the RouteLimitConfig definition to check algorithm enum.
	// Navigate through routes -> items -> rate_limit -> algorithm.
	routesSchema, ok := schema.Properties.Get("routes")
	if !ok {
		t.Fatal("schema missing \"routes\" property")
	}

	// routes is an array; get its items schema.
	if routesSchema.Items == nil {
		t.Fatal("routes schema has no items")
	}
	routeSchema := resolveRef(t, schema, routesSchema.Items)
	if routeSchema.Properties == nil {
		t.Fatal("route item schema has no properties")
	}

	rateLimitSchema, ok := routeSchema.Properties.Get("rate_limit")
	if !ok {
		t.Fatal("route schema missing \"rate_limit\" property")
	}
	rateLimitResolved := resolveRef(t, schema, rateLimitSchema)
	if rateLimitResolved.Properties == nil {
		t.Fatal("rate_limit schema has no properties")
	}

	algorithmSchema, ok := rateLimitResolved.Properties.Get("algorithm")
	if !ok {
		t.Fatal("rate_limit schema missing \"algorithm\" property")
	}
	algorithmResolved := resolveRef(t, schema, algorithmSchema)

	if len(algorithmResolved.Enum) == 0 {
		t.Fatal("algorithm schema has no enum values")
	}

	enumValues := make(map[string]bool)
	for _, v := range algorithmResolved.Enum {
		if s, ok := v.(string); ok {
			enumValues[s] = true
		}
	}

	for _, want := range []string{"sliding_window", "leaky_bucket"} {
		if !enumValues[want] {
			t.Errorf("algorithm enum missing value %q, got %v", want, algorithmResolved.Enum)
		}
	}
}

func TestGenerateSchemaJSON_ValidJSON(t *testing.T) {
	data, err := GenerateSchemaJSON()
	if err != nil {
		t.Fatalf("GenerateSchemaJSON returned error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("GenerateSchemaJSON returned empty data")
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("GenerateSchemaJSON output is not valid JSON: %v", err)
	}

	// Verify it has expected top-level JSON Schema keys.
	if _, ok := raw["$schema"]; !ok {
		t.Error("JSON output missing \"$schema\" key")
	}
	if _, ok := raw["properties"]; !ok {
		t.Error("JSON output missing \"properties\" key")
	}
}

func TestValidateAgainstSchema_ValidConfig(t *testing.T) {
	// Build a fully-populated config so that no pointer fields marshal as
	// null (the generated schema types pointer fields as objects, not null).
	cfg := &GatewayConfig{
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Logging: LoggingConfig{
			Level: "info",
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
			MaxAge:         3600,
		},
		RateLimit: RateLimitGlobalConfig{
			Backend: "memory",
		},
		Routes: []RouteConfig{
			{
				Name: "test-route",
				Match: MatchConfig{
					PathRegex: "^/api",
					Methods:   []string{"GET"},
				},
				Upstream: UpstreamConfig{
					URL:     "http://localhost:8081",
					Timeout: 30 * time.Second,
				},
			},
		},
	}

	errs := validateAgainstSchema(cfg)
	if len(errs) > 0 {
		t.Errorf("expected no validation errors for valid config, got: %v", errs)
	}
}

func TestValidateAgainstSchema_InvalidEnum(t *testing.T) {
	cfg := &GatewayConfig{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Logging: LoggingConfig{
			Level: "info",
		},
		RateLimit: RateLimitGlobalConfig{
			Backend: "postgres", // invalid: must be "memory" or "redis"
		},
		Routes: []RouteConfig{},
	}

	errs := validateAgainstSchema(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors for invalid backend enum, got none")
	}

	found := false
	for _, e := range errs {
		if strings.Contains(e, "backend") || strings.Contains(e, "postgres") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error mentioning backend or postgres, got: %v", errs)
	}
}

func TestValidateAgainstSchema_InvalidMinimum(t *testing.T) {
	cfg := &GatewayConfig{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 0, // invalid: minimum=1
		},
		Logging: LoggingConfig{
			Level: "info",
		},
		RateLimit: RateLimitGlobalConfig{
			Backend: "memory",
		},
		Routes: []RouteConfig{},
	}

	errs := validateAgainstSchema(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors for port=0, got none")
	}

	found := false
	for _, e := range errs {
		if strings.Contains(e, "port") || strings.Contains(e, "minimum") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error mentioning port or minimum, got: %v", errs)
	}
}

func TestGenerateSchema_RequiredFields(t *testing.T) {
	schema := GenerateSchema()

	// GatewayConfig should require "routes".
	if !containsString(schema.Required, "routes") {
		t.Errorf("GatewayConfig schema.Required = %v, expected it to contain \"routes\"", schema.Required)
	}

	// RouteConfig should require "name", "match", "upstream".
	routesSchema, ok := schema.Properties.Get("routes")
	if !ok {
		t.Fatal("schema missing \"routes\" property")
	}
	if routesSchema.Items == nil {
		t.Fatal("routes schema has no items")
	}
	routeSchema := resolveRef(t, schema, routesSchema.Items)
	for _, field := range []string{"name", "match", "upstream"} {
		if !containsString(routeSchema.Required, field) {
			t.Errorf("RouteConfig schema.Required = %v, expected it to contain %q", routeSchema.Required, field)
		}
	}

	// UpstreamConfig should require "url".
	upstreamSchema, ok := routeSchema.Properties.Get("upstream")
	if !ok {
		t.Fatal("route schema missing \"upstream\" property")
	}
	upstreamResolved := resolveRef(t, schema, upstreamSchema)
	if !containsString(upstreamResolved.Required, "url") {
		t.Errorf("UpstreamConfig schema.Required = %v, expected it to contain \"url\"", upstreamResolved.Required)
	}

	// MatchConfig should require "path_regex".
	matchSchema, ok := routeSchema.Properties.Get("match")
	if !ok {
		t.Fatal("route schema missing \"match\" property")
	}
	matchResolved := resolveRef(t, schema, matchSchema)
	if !containsString(matchResolved.Required, "path_regex") {
		t.Errorf("MatchConfig schema.Required = %v, expected it to contain \"path_regex\"", matchResolved.Required)
	}
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func TestGenerateSchema_AdditionalPropertiesFalse(t *testing.T) {
	s := GenerateSchema()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ap, ok := raw["additionalProperties"]
	if !ok {
		t.Fatal("expected additionalProperties in schema")
	}
	if ap != false {
		t.Errorf("expected additionalProperties=false, got %v", ap)
	}
}

// resolveRef is a test helper that follows a $ref to the corresponding $defs entry.
// If the schema has no $ref, it is returned as-is.
func resolveRef(t *testing.T, root, s *jsonschema.Schema) *jsonschema.Schema {
	t.Helper()
	if s == nil {
		t.Fatal("resolveRef: schema is nil")
	}
	if s.Ref == "" {
		return s
	}
	// Refs look like "#/$defs/ServerConfig"
	const prefix = "#/$defs/"
	if !strings.HasPrefix(s.Ref, prefix) {
		t.Fatalf("unexpected $ref format: %q", s.Ref)
	}
	defName := strings.TrimPrefix(s.Ref, prefix)
	def, ok := root.Definitions[defName]
	if !ok {
		t.Fatalf("$defs missing definition for %q", defName)
	}
	return def
}
