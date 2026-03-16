package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ResolveMiddlewares resolves a list of MiddlewareRef against the given
// definitions map. For each ref it looks up the definition, merges any
// per-ref config overrides, and decodes into the typed config struct.
func ResolveMiddlewares(refs []MiddlewareRef, defs map[string]MiddlewareDefinition) ([]ResolvedMiddleware, error) {
	resolved := make([]ResolvedMiddleware, 0, len(refs))
	for _, ref := range refs {
		def, ok := defs[ref.Ref]
		if !ok {
			return nil, fmt.Errorf("middleware ref %q: not found in middleware_definitions", ref.Ref)
		}

		cfgNode := def.Config
		if ref.Config.Kind != 0 {
			merged, err := mergeYAMLNodes(def.Config, ref.Config)
			if err != nil {
				return nil, fmt.Errorf("middleware ref %q: merging config override: %w", ref.Ref, err)
			}
			cfgNode = merged
		}

		typedCfg, err := decodeTypedConfig(def.Type, cfgNode)
		if err != nil {
			return nil, fmt.Errorf("middleware ref %q: decoding config for type %q: %w", ref.Ref, def.Type, err)
		}

		resolved = append(resolved, ResolvedMiddleware{
			Name:   ref.Ref,
			Type:   def.Type,
			Config: typedCfg,
		})
	}
	return resolved, nil
}

// mergeYAMLNodes performs a SHALLOW merge of override mapping keys into a
// copy of base. Both nodes must be mapping nodes (Kind == yaml.MappingNode).
//
// IMPORTANT: When an override key exists in base, the ENTIRE value subtree is
// replaced — nested mappings are NOT recursively merged. For example, if base
// has {request: {headers: {...}, body: {...}}} and override has
// {request: {headers: {...}}}, the merged result will only contain the
// override's request value — the body key from base is lost.
//
// Keys only in base are preserved. Keys only in override are added.
func mergeYAMLNodes(base, override yaml.Node) (yaml.Node, error) {
	if base.Kind == 0 {
		// Base has no config; use override as-is.
		return override, nil
	}
	if base.Kind != yaml.MappingNode {
		return yaml.Node{}, fmt.Errorf("base node is not a mapping (kind=%d)", base.Kind)
	}
	if override.Kind != yaml.MappingNode {
		return yaml.Node{}, fmt.Errorf("override node is not a mapping (kind=%d)", override.Kind)
	}

	// Build a map of base keys to their value indices.
	// MappingNode content is [key0, val0, key1, val1, ...].
	result := yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  base.Tag,
	}

	// Copy base content.
	result.Content = make([]*yaml.Node, len(base.Content))
	for i, n := range base.Content {
		cp := *n
		result.Content[i] = &cp
	}

	// Build index of base key positions.
	baseIndex := make(map[string]int, len(result.Content)/2)
	for i := 0; i < len(result.Content)-1; i += 2 {
		baseIndex[result.Content[i].Value] = i
	}

	// Apply override keys.
	for i := 0; i < len(override.Content)-1; i += 2 {
		key := override.Content[i].Value
		if idx, exists := baseIndex[key]; exists {
			// Replace existing value.
			valCopy := *override.Content[i+1]
			result.Content[idx+1] = &valCopy
		} else {
			// Add new key-value pair.
			keyCopy := *override.Content[i]
			valCopy := *override.Content[i+1]
			result.Content = append(result.Content, &keyCopy, &valCopy)
		}
	}

	return result, nil
}

// decodeTypedConfig decodes a yaml.Node into the typed struct based on the
// middleware type name.
func decodeTypedConfig(typeName string, node yaml.Node) (any, error) {
	switch typeName {
	case "cors":
		var cfg CORSConfig
		if node.Kind != 0 {
			if err := node.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("decoding cors config: %w", err)
			}
		}
		return &cfg, nil

	case "rate_limit":
		var cfg RateLimitConfig
		if node.Kind != 0 {
			if err := node.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("decoding rate_limit config: %w", err)
			}
		}
		applyRateLimitDefaults(&cfg)
		return &cfg, nil

	case "auth":
		var cfg AuthMiddlewareConfig
		if node.Kind != 0 {
			if err := node.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("decoding auth config: %w", err)
			}
		}
		return &cfg, nil

	case "transform":
		var cfg TransformConfig
		if node.Kind != 0 {
			if err := node.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("decoding transform config: %w", err)
			}
		}
		return &cfg, nil

	default:
		return nil, fmt.Errorf("unknown middleware type %q", typeName)
	}
}

// applyRateLimitDefaults applies default values to a decoded RateLimitConfig.
func applyRateLimitDefaults(cfg *RateLimitConfig) {
	if cfg.Algorithm == "" {
		cfg.Algorithm = "sliding_window"
	}
}
