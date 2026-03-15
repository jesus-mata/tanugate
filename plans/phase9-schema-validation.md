# Phase 9: JSON Schema Generation, Schema Validation & CLI Commands

## Context

The gateway config is validated at runtime by `GatewayConfig.Validate()` in `internal/config/config.go`. This works well for catching semantic errors at startup, but offers no structural validation against a schema, no IDE autocomplete, no pre-deployment validation CLI, and no way to validate config without starting the server. Roadmap item #19 explicitly requests a `gateway validate` subcommand.

The plan: generate a JSON Schema from Go structs using `invopop/jsonschema`, validate config against that schema as the first step in `Validate()` using `santhosh-tekuri/jsonschema/v6`, then run existing semantic checks. Also add `gateway validate` and `gateway schema` CLI commands.

## Validation layers

```
Validate() method:
  1. JSON Schema validation (structural: types, enums, min/max, patterns, required fields)
     └─ catches: wrong types, unknown enum values, out-of-range numbers, missing fields
  2. Programmatic validation (semantic: cross-field, regex compilation, reference checks)
     └─ catches: undefined auth providers, invalid regex, "none" + other providers, etc.
```

## Files to modify

| File | Action |
|------|--------|
| `internal/config/config.go` | Add `jsonschema` tags to structs; call `validateAgainstSchema()` in `Validate()` |
| `internal/config/schema.go` | **New** — schema generation, compilation, validation |
| `internal/config/schema_test.go` | **New** — tests |
| `cmd/gateway/main.go` | Add subcommand routing (`validate`, `schema`) |
| `Makefile` | Add `schema` target |
| `gateway-schema.json` | **New** — generated artifact, committed |
| `go.mod` / `go.sum` | Add `invopop/jsonschema` + `santhosh-tekuri/jsonschema/v6` |

## Step 1: Add dependencies

```
go get github.com/invopop/jsonschema
go get github.com/santhosh-tekuri/jsonschema/v6
```

- `invopop/jsonschema` — generates JSON Schema from Go structs (rich tag support, `FieldNameTag: "yaml"`)
- `santhosh-tekuri/jsonschema/v6` — validates JSON data against a JSON Schema (Draft 2020-12, structured errors with field paths)

## Step 2: Add `jsonschema` tags to config structs

Add `jsonschema` tags alongside existing `yaml` tags in `internal/config/config.go`. Only add tags that provide value (enums, defaults, min/max, descriptions for non-obvious fields). Do NOT add `json` tags — `FieldNameTag: "yaml"` on the reflector handles property naming.

Key tags to add:

**ServerConfig:**
- `Port` → `jsonschema:"default=8080,minimum=1,maximum=65535"`
- `Host` → `jsonschema:"default=0.0.0.0"`
- Duration fields → description noting format (type mapping handles the schema type)

**LoggingConfig:**
- `Level` → `jsonschema:"default=info,enum=debug,enum=info,enum=warn,enum=error"`

**RateLimitGlobalConfig:**
- `Backend` → `jsonschema:"default=memory,enum=memory,enum=redis"`

**AuthProvider:**
- `Type` → `jsonschema:"enum=jwt,enum=apikey,enum=oidc"`

**RouteLimitConfig:**
- `RequestsPerWindow` → `jsonschema:"minimum=1"`
- `Algorithm` → `jsonschema:"default=sliding_window,enum=sliding_window,enum=leaky_bucket"`

**RetryConfig:**
- `MaxRetries` → `jsonschema:"minimum=1"`
- `Multiplier` → `jsonschema:"minimum=1"`

**CircuitBreakerConfig:**
- `FailureThreshold` → `jsonschema:"minimum=1"`
- `SuccessThreshold` → `jsonschema:"minimum=1"`

**CORSConfig:**
- `MaxAge` → `jsonschema:"minimum=0"`

## Step 3: Create `internal/config/schema.go`

All schema logic lives in the `config` package to avoid circular imports.

```go
package config

// GenerateSchema returns the JSON Schema for GatewayConfig.
func GenerateSchema() *invopop.Schema { ... }

// GenerateSchemaJSON returns indented JSON bytes.
func GenerateSchemaJSON() ([]byte, error) { ... }

// validateAgainstSchema validates cfg against the JSON Schema.
// Returns field-level error strings, or nil if valid.
func validateAgainstSchema(cfg *GatewayConfig) []string { ... }
```

Key design:
- `Reflector.FieldNameTag = "yaml"` — property names match YAML keys
- `Reflector.Mapper` intercepts `time.Duration` → returns `{type: "string", pattern: "...", examples: ["30s","1m"]}` since Duration is `int64` internally but users write `"30s"` in YAML
- **Compiled schema cached at package level** via `sync.Once` — the schema is deterministic (derived from struct types), so compile once and reuse
- `validateAgainstSchema()` — **unexported**, called from `Validate()`
- YAML round-trip for correct field names since structs have `yaml` tags but no `json` tags:
  ```go
  yamlBytes, _ := yaml.Marshal(cfg)
  var instance any
  yaml.Unmarshal(yamlBytes, &instance)
  // Now field names match yaml tag names used in schema
  ```

## Step 4: Integrate schema validation into `Validate()`

```go
func (cfg *GatewayConfig) Validate() error {
    var errs []string

    // Layer 1: Validate against JSON Schema (structural).
    errs = append(errs, validateAgainstSchema(cfg)...)

    // Layer 2: Semantic validation (existing checks).
    for _, route := range cfg.Routes {
        // ... unchanged ...
    }

    if len(errs) > 0 {
        return fmt.Errorf("config validation failed: %s", strings.Join(errs, "; "))
    }
    return nil
}
```

Both layers run and all errors are collected — no short-circuit on schema errors so the user sees everything at once.

## Step 5: Create `internal/config/schema_test.go`

Tests:
- Schema has correct `$schema` version (Draft 2020-12)
- Top-level properties match yaml tag names (`server`, `logging`, `cors`, `rate_limit`, `auth_providers`, `routes`)
- `time.Duration` fields are `type: "string"` with pattern
- Enum fields have correct values (e.g., `algorithm` → `["sliding_window", "leaky_bucket"]`)
- `GenerateSchemaJSON()` produces valid JSON
- `validateAgainstSchema` with a valid config → no errors
- `validateAgainstSchema` with invalid enum value → error mentioning the field
- `validateAgainstSchema` with out-of-range number → error mentioning the field

## Step 6: Restructure CLI with subcommand routing

Modify `cmd/gateway/main.go`. Use `os.Args`-based routing (no Cobra):

```go
func main() {
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "validate":
            runValidate()
            return
        case "schema":
            runSchema()
            return
        }
    }
    runServe() // existing main() body, preserves backward compat
}
```

**`runValidate()`** — uses `flag.NewFlagSet("validate", ...)` with `-config` flag. Calls `config.LoadConfig()`, prints "OK" or errors, exits 0 or 1.

**`runSchema()`** — calls `config.GenerateSchemaJSON()`, writes to stdout.

**`runServe()`** — the entire current `main()` body extracted into a function. `flag.Parse()` stays as-is.

**Backward compatibility:** `gateway -config path` still works (falls through to `runServe()`). Dockerfile ENTRYPOINT unchanged.

## Step 7: Add Makefile target

```makefile
schema:
	go run ./cmd/gateway schema > gateway-schema.json
```

## Step 8: Generate and commit `gateway-schema.json`

Run `make schema` to produce the initial file at the repo root. Commit it for IDE integration:

```yaml
# At top of YAML config file:
# yaml-language-server: $schema=./gateway-schema.json
```

## Verification

1. `go test ./internal/config/...` — all tests pass (existing + new schema tests)
2. `make build && ./bin/gateway validate -config config/gateway.example.yaml` → prints "OK", exits 0
3. `./bin/gateway validate -config /dev/null` → prints error, exits 1
4. `./bin/gateway schema | python3 -m json.tool` → valid JSON schema output
5. `./bin/gateway -config config/gateway.yaml` → server starts normally (backward compat)
6. `make schema && git diff --exit-code gateway-schema.json` → no drift
7. Manually test with a config that has a bad enum value (e.g., `backend: "postgres"`) → schema validation catches it before semantic checks
