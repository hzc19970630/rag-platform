package model

import (
	"strings"
	"testing"
)

func validToolSpec() ToolSpec {
	return ToolSpec{
		Name:        "table_query",
		Description: "Query one table",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			},
			"required": []any{"question"},
		},
	}
}

func TestValidateToolSpecs(t *testing.T) {
	if err := ValidateToolSpecs([]ToolSpec{validToolSpec()}); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	duplicate := validToolSpec()
	if err := ValidateToolSpecs([]ToolSpec{validToolSpec(), duplicate}); err == nil || !strings.Contains(err.Error(), "registered twice") {
		t.Fatalf("duplicate spec error = %v", err)
	}
	badName := validToolSpec()
	badName.Name = "1bad"
	if err := ValidateToolSpec(badName); err == nil {
		t.Fatal("bad name accepted")
	}
	noDescription := validToolSpec()
	noDescription.Description = " "
	if err := ValidateToolSpec(noDescription); err == nil {
		t.Fatal("empty description accepted")
	}
	notObject := validToolSpec()
	notObject.InputSchema = map[string]any{"type": "string"}
	if err := ValidateToolSpec(notObject); err == nil {
		t.Fatal("non-object schema accepted")
	}
	badProperty := validToolSpec()
	badProperty.InputSchema = map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "null"}},
	}
	if err := ValidateToolSpec(badProperty); err == nil {
		t.Fatal("unsupported property type accepted")
	}
	unknownRequired := validToolSpec()
	unknownRequired.InputSchema = map[string]any{
		"type": "object", "properties": map[string]any{},
		"required": []any{"missing"},
	}
	if err := ValidateToolSpec(unknownRequired); err == nil {
		t.Fatal("unknown required property accepted")
	}
}

func TestValidateToolArguments(t *testing.T) {
	spec := validToolSpec()
	if err := ValidateToolArguments(spec, `{"question":"sum","limit":3}`); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	if err := ValidateToolArguments(spec, `{}`); err == nil {
		t.Fatal("missing required argument accepted")
	}
	if err := ValidateToolArguments(spec, `{"question":"sum","limit":1.5}`); err == nil {
		t.Fatal("non-integer accepted for integer property")
	}
	if err := ValidateToolArguments(spec, `{"question":1}`); err == nil {
		t.Fatal("non-string accepted for string property")
	}
	if err := ValidateToolArguments(spec, `{"question":"sum"} trailing`); err == nil {
		t.Fatal("trailing data accepted")
	}
	if err := ValidateToolArguments(spec, ``); err == nil {
		t.Fatal("empty arguments should miss the required property")
	}
}
