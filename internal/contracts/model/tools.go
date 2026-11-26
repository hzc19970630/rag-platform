package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrToolsUnsupported is returned when a model cannot accept tool specs. The
// caller falls back to the deterministic table router instead of retrying.
var ErrToolsUnsupported = errors.New("tools_not_supported")

// ToolCapable is implemented by adapters that can render tool calls. Absence
// or false means the caller must use the deterministic fallback.
type ToolCapable interface {
	SupportsTools() bool
}

// ChatterSupportsTools reports whether the adapter can accept tool specs.
func ChatterSupportsTools(chatter Chatter) bool {
	capable, ok := chatter.(ToolCapable)
	return ok && capable.SupportsTools()
}

// ToolCapabilityResolver is implemented by routing adapters whose capability
// depends on the bound model rather than the deployment default.
type ToolCapabilityResolver interface {
	SupportsToolCalls(ctx context.Context, model string) bool
}

// ChatterSupportsToolsFor resolves tool capability for one request model.
func ChatterSupportsToolsFor(ctx context.Context, chatter Chatter, model string) bool {
	if resolver, ok := chatter.(ToolCapabilityResolver); ok {
		return resolver.SupportsToolCalls(ctx, model)
	}
	return ChatterSupportsTools(chatter)
}

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

var toolSchemaTypes = map[string]bool{
	"string": true, "number": true, "integer": true,
	"boolean": true, "array": true, "object": true,
}

// ValidateToolSpecs rejects duplicate names and schemas outside the subset the
// adapters and tool registry guarantee to understand.
func ValidateToolSpecs(specs []ToolSpec) error {
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if seen[spec.Name] {
			return fmt.Errorf("tool %q is registered twice", spec.Name)
		}
		seen[spec.Name] = true
		if err := ValidateToolSpec(spec); err != nil {
			return err
		}
	}
	return nil
}

// ValidateToolSpec checks one tool definition. The schema must describe an
// object; property types may only use the primitive set adapters can render.
func ValidateToolSpec(spec ToolSpec) error {
	if !toolNamePattern.MatchString(spec.Name) {
		return fmt.Errorf("tool name %q must start with a letter and contain only letters, digits, _ or -", spec.Name)
	}
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("tool %q must have a description", spec.Name)
	}
	if spec.InputSchema == nil {
		return fmt.Errorf("tool %q must have an input schema", spec.Name)
	}
	kind, _ := spec.InputSchema["type"].(string)
	if kind != "object" {
		return fmt.Errorf("tool %q input schema type must be object", spec.Name)
	}
	properties := map[string]any{}
	switch value := spec.InputSchema["properties"].(type) {
	case nil:
	case map[string]any:
		properties = value
	default:
		return fmt.Errorf("tool %q properties must be an object", spec.Name)
	}
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("tool %q property %q must be a schema object", spec.Name, name)
		}
		propertyType, _ := property["type"].(string)
		if propertyType != "" && !toolSchemaTypes[propertyType] {
			return fmt.Errorf("tool %q property %q has unsupported type %q", spec.Name, name, propertyType)
		}
	}
	for _, name := range schemaRequired(spec.InputSchema) {
		if _, ok := properties[name]; !ok {
			return fmt.Errorf("tool %q requires unknown property %q", spec.Name, name)
		}
	}
	return nil
}

// ValidateToolArguments checks one model-produced argument object against the
// declared schema. Unknown extra keys are ignored; the tool executor decides
// whether they are safe.
func ValidateToolArguments(spec ToolSpec, raw string) error {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil {
		return fmt.Errorf("tool %q arguments are not a JSON object: %w", spec.Name, err)
	}
	if decoder.More() {
		return fmt.Errorf("tool %q arguments contain trailing data", spec.Name)
	}
	if arguments == nil {
		return fmt.Errorf("tool %q arguments must be an object", spec.Name)
	}
	for _, name := range schemaRequired(spec.InputSchema) {
		if _, ok := arguments[name]; !ok {
			return fmt.Errorf("tool %q is missing required argument %q", spec.Name, name)
		}
	}
	properties, _ := spec.InputSchema["properties"].(map[string]any)
	for name, value := range arguments {
		rawProperty, ok := properties[name]
		if !ok {
			continue
		}
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := property["type"].(string)
		if kind == "" {
			continue
		}
		if !argumentTypeMatches(kind, value) {
			return fmt.Errorf("tool %q argument %q must be %s", spec.Name, name, kind)
		}
	}
	return nil
}

func schemaRequired(schema map[string]any) []string {
	switch value := schema["required"].(type) {
	case []any:
		names := make([]string, 0, len(value))
		for _, item := range value {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	case []string:
		return value
	default:
		return nil
	}
}

func argumentTypeMatches(kind string, value any) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		return !strings.ContainsAny(number.String(), ".eE")
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}
