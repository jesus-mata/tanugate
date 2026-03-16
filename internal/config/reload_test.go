package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestValidate_InvalidPathRegex(t *testing.T) {
	yaml := `
routes:
  - name: "bad-regex"
    match:
      path_regex: "^/api/[invalid"
    upstream:
      url: "http://localhost:8080"
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
	if !strings.Contains(err.Error(), "invalid path_regex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNonReloadableChanges_ServerChanges(t *testing.T) {
	old := &GatewayConfig{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8080, ReadTimeout: 30 * time.Second},
	}
	new := &GatewayConfig{
		Server: ServerConfig{Host: "127.0.0.1", Port: 9090, ReadTimeout: 60 * time.Second},
	}

	warnings := NonReloadableChanges(old, new)
	if len(warnings) < 3 {
		t.Fatalf("expected at least 3 warnings, got %d: %v", len(warnings), warnings)
	}

	found := map[string]bool{"host": false, "port": false, "read_timeout": false}
	for _, w := range warnings {
		for key := range found {
			if strings.Contains(w, "server."+key) {
				found[key] = true
			}
		}
	}
	for key, ok := range found {
		if !ok {
			t.Errorf("expected warning for server.%s", key)
		}
	}
}

func TestNonReloadableChanges_BackendChange(t *testing.T) {
	old := &GatewayConfig{
		RateLimit: RateLimitGlobalConfig{Backend: "memory"},
	}
	new := &GatewayConfig{
		RateLimit: RateLimitGlobalConfig{Backend: "redis"},
	}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "rate_limit.backend") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for rate_limit.backend change, got %v", warnings)
	}
}

func TestNonReloadableChanges_RedisChange(t *testing.T) {
	old := &GatewayConfig{
		RateLimit: RateLimitGlobalConfig{
			Backend: "redis",
			Redis:   &RedisConfig{Addr: "localhost:6379"},
		},
	}
	new := &GatewayConfig{
		RateLimit: RateLimitGlobalConfig{
			Backend: "redis",
			Redis:   &RedisConfig{Addr: "redis:6379"},
		},
	}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "rate_limit.redis") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for rate_limit.redis change, got %v", warnings)
	}
}

func TestNonReloadableChanges_RedisNewFieldsChange(t *testing.T) {
	base := RedisConfig{
		Addr:         "localhost:6379",
		Password:     "pass",
		DB:           0,
		PoolSize:     10,
		MinIdleConns: 2,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   1,
		QueryTimeout: 100 * time.Millisecond,
		TLSEnabled:   false,
	}

	// Changing PoolSize alone should trigger a warning.
	changed := base
	changed.PoolSize = 20

	old := &GatewayConfig{RateLimit: RateLimitGlobalConfig{Backend: "redis", Redis: &base}}
	new := &GatewayConfig{RateLimit: RateLimitGlobalConfig{Backend: "redis", Redis: &changed}}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "rate_limit.redis") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning when only PoolSize changed, got %v", warnings)
	}

	// Changing TLSEnabled alone should also trigger a warning.
	changed2 := base
	changed2.TLSEnabled = true
	new2 := &GatewayConfig{RateLimit: RateLimitGlobalConfig{Backend: "redis", Redis: &changed2}}

	warnings2 := NonReloadableChanges(old, new2)
	found2 := false
	for _, w := range warnings2 {
		if strings.Contains(w, "rate_limit.redis") {
			found2 = true
		}
	}
	if !found2 {
		t.Errorf("expected warning when only TLSEnabled changed, got %v", warnings2)
	}
}

func TestNonReloadableChanges_AuthProviderChange(t *testing.T) {
	old := &GatewayConfig{
		AuthProviders: map[string]AuthProvider{
			"jwt_default": {Type: "jwt", JWT: &JWTConfig{Secret: "old-secret", Algorithm: "HS256"}},
		},
	}
	new := &GatewayConfig{
		AuthProviders: map[string]AuthProvider{
			"jwt_default": {Type: "jwt", JWT: &JWTConfig{Secret: "new-secret", Algorithm: "HS256"}},
		},
	}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "auth_providers") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for auth_providers change, got %v", warnings)
	}
}

func TestNonReloadableChanges_LoggingLevelChange(t *testing.T) {
	old := &GatewayConfig{Logging: LoggingConfig{Level: "info"}}
	new := &GatewayConfig{Logging: LoggingConfig{Level: "debug"}}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "logging.level") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for logging.level change, got %v", warnings)
	}
}

func TestNonReloadableChanges_TrustedProxiesChange(t *testing.T) {
	old := &GatewayConfig{
		Server: ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}},
	}
	new := &GatewayConfig{
		Server: ServerConfig{TrustedProxies: []string{"172.16.0.0/12"}},
	}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "trusted_proxies") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for trusted_proxies change, got %v", warnings)
	}
}

func TestNonReloadableChanges_MaxHeaderBytesChange(t *testing.T) {
	old := &GatewayConfig{
		Server: ServerConfig{MaxHeaderBytes: 1 << 20},
	}
	new := &GatewayConfig{
		Server: ServerConfig{MaxHeaderBytes: 8192},
	}

	warnings := NonReloadableChanges(old, new)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "max_header_bytes") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for max_header_bytes change, got %v", warnings)
	}
}

func TestNonReloadableChanges_NoChanges(t *testing.T) {
	cfg := &GatewayConfig{
		Server:    ServerConfig{Host: "0.0.0.0", Port: 8080},
		Logging:   LoggingConfig{Level: "info"},
		RateLimit: RateLimitGlobalConfig{Backend: "memory"},
	}

	warnings := NonReloadableChanges(cfg, cfg)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for identical config, got %v", warnings)
	}
}

func TestDiffSummary_RouteAdded(t *testing.T) {
	old := &GatewayConfig{}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "new-route", Match: MatchConfig{PathRegex: "^/new"}},
		},
	}

	changes := DiffSummary(old, new)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if !strings.Contains(changes[0], "added") {
		t.Errorf("expected 'added' in change, got %q", changes[0])
	}
}

func TestDiffSummary_RouteRemoved(t *testing.T) {
	old := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "old-route", Match: MatchConfig{PathRegex: "^/old"}},
		},
	}
	new := &GatewayConfig{}

	changes := DiffSummary(old, new)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if !strings.Contains(changes[0], "removed") {
		t.Errorf("expected 'removed' in change, got %q", changes[0])
	}
}

func TestDiffSummary_RouteModified(t *testing.T) {
	old := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "api", Match: MatchConfig{PathRegex: "^/api"}, Upstream: UpstreamConfig{URL: "http://old:8080"}},
		},
	}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "api", Match: MatchConfig{PathRegex: "^/api"}, Upstream: UpstreamConfig{URL: "http://new:8080"}},
		},
	}

	changes := DiffSummary(old, new)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if !strings.Contains(changes[0], "modified") {
		t.Errorf("expected 'modified' in change, got %q", changes[0])
	}
}

func TestDiffSummary_CORSMiddlewareDefinitionChanged(t *testing.T) {
	buildNode := func(yamlStr string) yaml.Node {
		var node yaml.Node
		_ = yaml.Unmarshal([]byte(yamlStr), &node)
		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			return *node.Content[0]
		}
		return node
	}

	old := &GatewayConfig{
		MiddlewareDefinitions: map[string]MiddlewareDefinition{
			"global-cors": {Type: "cors", Config: buildNode("allowed_origins:\n  - https://old.example.com")},
		},
	}
	new := &GatewayConfig{
		MiddlewareDefinitions: map[string]MiddlewareDefinition{
			"global-cors": {Type: "cors", Config: buildNode("allowed_origins:\n  - https://new.example.com")},
		},
	}

	changes := DiffSummary(old, new)
	found := false
	for _, c := range changes {
		if strings.Contains(c, "middleware_definitions") && strings.Contains(c, "modified") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected middleware_definitions modified change in diff, got %v", changes)
	}
}

func TestDiffSummary_NoChanges(t *testing.T) {
	cfg := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "api", Match: MatchConfig{PathRegex: "^/api"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
		},
	}

	changes := DiffSummary(cfg, cfg)
	if len(changes) != 0 {
		t.Fatalf("expected no changes for identical config, got %v", changes)
	}
}

func TestDiffSummary_MiddlewareRefsModified(t *testing.T) {
	// Test route modification detection via middleware ref changes.
	old := &GatewayConfig{
		Routes: []RouteConfig{
			{
				Name:     "api",
				Match:    MatchConfig{PathRegex: "^/api"},
				Upstream: UpstreamConfig{URL: "http://svc:8080"},
				Middlewares: []MiddlewareRef{
					{Ref: "rl"},
				},
			},
		},
	}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{
				Name:     "api",
				Match:    MatchConfig{PathRegex: "^/api"},
				Upstream: UpstreamConfig{URL: "http://svc:8080"},
				Middlewares: []MiddlewareRef{
					{Ref: "rl"},
					{Ref: "auth"},
				},
			},
		},
	}

	changes := DiffSummary(old, new)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if !strings.Contains(changes[0], "modified") {
		t.Errorf("expected 'modified' in change, got %q", changes[0])
	}
}

func TestDiffSummary_SkipMiddlewaresModified(t *testing.T) {
	old := &GatewayConfig{
		Routes: []RouteConfig{
			{
				Name:     "api",
				Match:    MatchConfig{PathRegex: "^/api"},
				Upstream: UpstreamConfig{URL: "http://svc:8080"},
			},
		},
	}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{
				Name:            "api",
				Match:           MatchConfig{PathRegex: "^/api"},
				Upstream:        UpstreamConfig{URL: "http://svc:8080"},
				SkipMiddlewares: []string{"rl"},
			},
		},
	}

	changes := DiffSummary(old, new)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if !strings.Contains(changes[0], "modified") {
		t.Errorf("expected 'modified' in change, got %q", changes[0])
	}
}

func TestDiffSummary_MiddlewareDefinitionsChanged(t *testing.T) {
	// Helper to build a yaml.Node for a config map
	buildNode := func(yamlStr string) yaml.Node {
		var node yaml.Node
		_ = yaml.Unmarshal([]byte(yamlStr), &node)
		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			return *node.Content[0]
		}
		return node
	}

	old := &GatewayConfig{
		MiddlewareDefinitions: map[string]MiddlewareDefinition{
			"rl": {Type: "rate_limit", Config: buildNode("requests: 100\nwindow: 60s")},
		},
	}
	new := &GatewayConfig{
		MiddlewareDefinitions: map[string]MiddlewareDefinition{
			"rl":   {Type: "rate_limit", Config: buildNode("requests: 200\nwindow: 60s")},
			"auth": {Type: "auth", Config: buildNode("providers: [none]")},
		},
	}

	changes := DiffSummary(old, new)
	foundModified := false
	foundAdded := false
	for _, c := range changes {
		if strings.Contains(c, "middleware_definitions") && strings.Contains(c, "modified") {
			foundModified = true
		}
		if strings.Contains(c, "middleware_definitions") && strings.Contains(c, "added") {
			foundAdded = true
		}
	}
	if !foundModified {
		t.Errorf("expected middleware_definitions modified change, got %v", changes)
	}
	if !foundAdded {
		t.Errorf("expected middleware_definitions added change, got %v", changes)
	}
}

func TestDiffSummary_GlobalMiddlewaresChanged(t *testing.T) {
	old := &GatewayConfig{
		Middlewares: []MiddlewareRef{
			{Ref: "rl"},
		},
	}
	new := &GatewayConfig{
		Middlewares: []MiddlewareRef{
			{Ref: "rl"},
			{Ref: "auth"},
		},
	}

	changes := DiffSummary(old, new)
	found := false
	for _, c := range changes {
		if strings.Contains(c, "global middlewares") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected global middlewares change, got %v", changes)
	}
}

func TestDiffSummary_RouteOrderChanged(t *testing.T) {
	old := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "api-a", Match: MatchConfig{PathRegex: "^/a"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
			{Name: "api-b", Match: MatchConfig{PathRegex: "^/b"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
		},
	}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "api-b", Match: MatchConfig{PathRegex: "^/b"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
			{Name: "api-a", Match: MatchConfig{PathRegex: "^/a"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
		},
	}

	changes := DiffSummary(old, new)
	found := false
	for _, c := range changes {
		if strings.Contains(c, "order") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected route order change in diff, got %v", changes)
	}
}

func TestDiffSummary_NestedMiddlewareConfigChanged(t *testing.T) {
	// buildNode unmarshals a YAML string into a yaml.Node (same pattern as existing tests).
	buildNode := func(yamlStr string) yaml.Node {
		var node yaml.Node
		_ = yaml.Unmarshal([]byte(yamlStr), &node)
		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			return *node.Content[0]
		}
		return node
	}

	old := &GatewayConfig{
		MiddlewareDefinitions: map[string]MiddlewareDefinition{
			"transform": {
				Type: "transform",
				Config: buildNode(`
request:
  headers:
    add:
      X-Foo: bar
`),
			},
		},
	}
	new := &GatewayConfig{
		MiddlewareDefinitions: map[string]MiddlewareDefinition{
			"transform": {
				Type: "transform",
				Config: buildNode(`
request:
  headers:
    add:
      X-Foo: baz
`),
			},
		},
	}

	changes := DiffSummary(old, new)
	found := false
	for _, c := range changes {
		if strings.Contains(c, "middleware_definitions") && strings.Contains(c, "modified") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected nested middleware definition change to be detected, got %v", changes)
	}
}

func TestDiffSummary_NestedMiddlewareRefOverrideChanged(t *testing.T) {
	buildNode := func(yamlStr string) yaml.Node {
		var node yaml.Node
		_ = yaml.Unmarshal([]byte(yamlStr), &node)
		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			return *node.Content[0]
		}
		return node
	}

	old := &GatewayConfig{
		Routes: []RouteConfig{
			{
				Name:     "api",
				Match:    MatchConfig{PathRegex: "^/api"},
				Upstream: UpstreamConfig{URL: "http://svc:8080"},
				Middlewares: []MiddlewareRef{
					{
						Ref: "transform",
						Config: buildNode(`
request:
  headers:
    add:
      X-Request-ID: old-value
`),
					},
				},
			},
		},
	}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{
				Name:     "api",
				Match:    MatchConfig{PathRegex: "^/api"},
				Upstream: UpstreamConfig{URL: "http://svc:8080"},
				Middlewares: []MiddlewareRef{
					{
						Ref: "transform",
						Config: buildNode(`
request:
  headers:
    add:
      X-Request-ID: new-value
`),
					},
				},
			},
		},
	}

	changes := DiffSummary(old, new)
	if len(changes) == 0 {
		t.Fatal("expected route modification due to nested middleware ref override change, got no changes")
	}
	found := false
	for _, c := range changes {
		if strings.Contains(c, "modified") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'modified' in change, got %v", changes)
	}
}

func TestDiffSummary_MultipleChanges(t *testing.T) {
	old := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "keep", Match: MatchConfig{PathRegex: "^/keep"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
			{Name: "remove-me", Match: MatchConfig{PathRegex: "^/remove"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
		},
	}
	new := &GatewayConfig{
		Routes: []RouteConfig{
			{Name: "keep", Match: MatchConfig{PathRegex: "^/keep"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
			{Name: "add-me", Match: MatchConfig{PathRegex: "^/add"}, Upstream: UpstreamConfig{URL: "http://svc:8080"}},
		},
	}

	changes := DiffSummary(old, new)
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (add + remove), got %d: %v", len(changes), changes)
	}

	foundAdd, foundRemove := false, false
	for _, c := range changes {
		if strings.Contains(c, "added") {
			foundAdd = true
		}
		if strings.Contains(c, "removed") {
			foundRemove = true
		}
	}
	if !foundAdd || !foundRemove {
		t.Errorf("expected add and remove changes, got %v", changes)
	}
}
