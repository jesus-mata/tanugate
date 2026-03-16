package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	invopop "github.com/invopop/jsonschema"
	validate "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// cachedSchema stores the generated schema so that reflection only runs once.
var (
	cachedSchema     *invopop.Schema
	cachedSchemaOnce sync.Once
)

// cachedCompiled stores the compiled validator schema. A Mutex is used instead
// of sync.Once so that compilation errors are retried on the next call.
var (
	cachedCompiled   *validate.Schema
	cachedCompiledMu sync.Mutex
)

// GenerateSchema generates a JSON Schema from GatewayConfig using reflection.
// Property names are derived from yaml struct tags so they match YAML keys.
// The result is cached via sync.Once since the schema is deterministic.
func GenerateSchema() *invopop.Schema {
	cachedSchemaOnce.Do(func() {
		r := &invopop.Reflector{
			FieldNameTag:               "yaml",
			DoNotReference:             false,
			ExpandedStruct:             true,
			AllowAdditionalProperties:  false,
			RequiredFromJSONSchemaTags: true,
		}

		// Map time.Duration to a string schema with a pattern and examples,
		// because Duration is int64 internally but users write "30s" in YAML.
		durationType := reflect.TypeOf(time.Duration(0))
		yamlNodeType := reflect.TypeOf(yaml.Node{})
		r.Mapper = func(t reflect.Type) *invopop.Schema {
			if t == durationType {
				return &invopop.Schema{
					Type:        "string",
					Pattern:     `^([0-9]+(ns|us|µs|ms|s|m|h))+$`,
					Description: "Go duration string (e.g. 30s, 1m, 2h30m)",
					Examples:    []any{"30s", "1m", "500ms", "2h30m"},
				}
			}
			if t == yamlNodeType {
				return &invopop.Schema{
					Description: "Type-specific middleware configuration",
				}
			}
			return nil
		}

		cachedSchema = r.Reflect(&GatewayConfig{})
	})
	return cachedSchema
}

// GenerateSchemaJSON returns the JSON Schema for GatewayConfig as indented
// JSON bytes.
func GenerateSchemaJSON() ([]byte, error) {
	s := GenerateSchema()
	return json.MarshalIndent(s, "", "  ")
}

// compileValidator compiles the generated JSON Schema into a validator schema
// that can be used repeatedly. The result is cached.
func compileValidator() (*validate.Schema, error) {
	cachedCompiledMu.Lock()
	defer cachedCompiledMu.Unlock()

	if cachedCompiled != nil {
		return cachedCompiled, nil
	}

	schemaJSON, err := GenerateSchemaJSON()
	if err != nil {
		return nil, fmt.Errorf("generating schema JSON: %w", err)
	}

	// Parse the schema JSON into an any value that the compiler can
	// consume. UnmarshalJSON from the santhosh-tekuri library preserves
	// json.Number precision.
	schemaDoc, err := validate.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("unmarshaling schema JSON: %w", err)
	}

	c := validate.NewCompiler()
	if err := c.AddResource("schema.json", schemaDoc); err != nil {
		return nil, fmt.Errorf("adding schema resource: %w", err)
	}
	compiled, err := c.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}

	cachedCompiled = compiled
	return cachedCompiled, nil
}

// validateAgainstSchema validates the given GatewayConfig against the generated
// JSON Schema. It returns a slice of human-readable error strings.
//
// The approach uses a YAML round-trip to produce a map[string]any whose keys
// match the yaml struct tags (and therefore the schema property names), then
// converts that to JSON so the validator sees the correct field names.
func validateAgainstSchema(cfg *GatewayConfig) []string {
	sch, err := compileValidator()
	if err != nil {
		return []string{fmt.Sprintf("schema compilation error: %v", err)}
	}

	// YAML round-trip: marshal the config then unmarshal into a generic
	// structure so that keys match yaml tags.
	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return []string{fmt.Sprintf("schema validation: failed to marshal config to YAML: %v", err)}
	}
	var instance any
	if err := yaml.Unmarshal(yamlBytes, &instance); err != nil {
		return []string{fmt.Sprintf("schema validation: failed to unmarshal YAML: %v", err)}
	}

	// Convert the YAML-decoded value to a JSON-compatible representation.
	// gopkg.in/yaml.v3 decodes maps as map[string]any which is what the
	// validator expects, but integer keys or non-string types in maps need
	// conversion. Also, numeric values must be json.Number for the validator.
	instance = yamlToJSONCompatible(instance)

	err = sch.Validate(instance)
	if err == nil {
		return nil
	}

	verr, ok := err.(*validate.ValidationError)
	if !ok {
		return []string{fmt.Sprintf("schema validation error: %v", err)}
	}

	return extractErrors(verr)
}

// ValidateRawConfig validates raw YAML bytes against the generated JSON Schema.
// Unlike validateAgainstSchema (which round-trips through a Go struct),
// this operates on the raw YAML so it can detect unknown fields (typos)
// and reject invalid values before defaults are applied.
func ValidateRawConfig(yamlBytes []byte) []string {
	sch, err := compileValidator()
	if err != nil {
		return []string{fmt.Sprintf("schema compilation error: %v", err)}
	}

	var instance any
	if err := yaml.Unmarshal(yamlBytes, &instance); err != nil {
		return []string{fmt.Sprintf("schema validation: YAML parse error: %v", err)}
	}

	instance = yamlToJSONCompatible(instance)

	err = sch.Validate(instance)
	if err == nil {
		return nil
	}

	verr, ok := err.(*validate.ValidationError)
	if !ok {
		return []string{fmt.Sprintf("schema validation error: %v", err)}
	}

	return extractErrors(verr)
}

// extractErrors walks a ValidationError tree and produces flat human-readable
// error strings. It uses BasicOutput() to get a flat list of errors.
func extractErrors(verr *validate.ValidationError) []string {
	basic := verr.BasicOutput()
	if basic == nil || basic.Valid {
		return nil
	}

	var errs []string
	for _, unit := range basic.Errors {
		if unit.Error == nil {
			continue
		}
		msg := unit.Error.String()
		if msg == "" {
			continue
		}
		loc := unit.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		errs = append(errs, fmt.Sprintf("%s: %s", loc, msg))
	}
	return errs
}

// yamlToJSONCompatible recursively converts a value decoded by gopkg.in/yaml.v3
// into a representation compatible with the JSON Schema validator:
//   - map[string]any keys are preserved; null values are dropped (nil pointer = absent)
//   - []any slices are recursed
//   - int/int64/float64 are converted to json.Number
//   - bool/string/nil are passed through
func yamlToJSONCompatible(v any) any {
	switch val := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(val))
		for k, v := range val {
			converted := yamlToJSONCompatible(v)
			if converted == nil {
				// Skip null values: a nil pointer in Go means the field
				// is absent, not explicitly set to null.
				continue
			}
			m[k] = converted
		}
		return m
	case []any:
		s := make([]any, len(val))
		for i, v := range val {
			s[i] = yamlToJSONCompatible(v)
		}
		return s
	case int:
		return json.Number(fmt.Sprintf("%d", val))
	case int64:
		return json.Number(fmt.Sprintf("%d", val))
	case uint64:
		return json.Number(fmt.Sprintf("%d", val))
	case float64:
		// Use %v to avoid trailing zeros for integer-valued floats.
		return json.Number(fmt.Sprintf("%v", val))
	default:
		return v
	}
}
