package main

import (
	"bytes"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	contract "github.com/example/rag-platform/contracts/httpapi"
	"gopkg.in/yaml.v3"
)

// TestGenerateIsDeterministic pins the drift gate: the same contracts must
// always render byte-identical YAML, otherwise -check would flap.
func TestGenerateIsDeterministic(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatalf("generate again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Generate is not deterministic")
	}
	document := generatedDocument(t)
	if document["openapi"] != "3.0.3" {
		t.Fatalf("openapi version = %v, want 3.0.3", document["openapi"])
	}
}

// TestGenerateCoversRouteManifest requires one operation per manifest entry
// and rejects an operation the manifest does not declare.
func TestGenerateCoversRouteManifest(t *testing.T) {
	document := generatedDocument(t)
	paths := objectAt(t, document, "paths")
	declared := map[string]bool{}
	operations := 0
	for _, route := range contract.Routes {
		key := route.Path + " " + strings.ToLower(route.Method)
		declared[key] = true
		operations++

		methods := objectAt(t, paths, route.Path)
		operation := objectAt(t, methods, strings.ToLower(route.Method))
		responses := objectAt(t, operation, "responses")
		if _, ok := responses[strconv.Itoa(route.Success.Status)]; !ok {
			t.Errorf("%s: success status %d is missing", key, route.Success.Status)
		}
		if route.Request != nil {
			body := objectAt(t, operation, "requestBody")
			content := objectAt(t, body, "content")
			contentType := route.Request.ContentType
			if contentType == "" {
				contentType = "application/json"
			}
			if _, ok := content[contentType]; !ok {
				t.Errorf("%s: request content type %q is missing", key, contentType)
			}
		}
	}
	if operations != len(contract.Routes) {
		t.Fatalf("operations = %d, manifest routes = %d", operations, len(contract.Routes))
	}
	count := 0
	for path, methods := range paths {
		methodsObject, ok := methods.(map[string]any)
		if !ok {
			t.Fatalf("path %q is not an object", path)
		}
		for method := range methodsObject {
			count++
			if !declared[path+" "+method] {
				t.Errorf("document declares %s %s but the manifest does not", strings.ToUpper(method), path)
			}
		}
	}
	if count != operations {
		t.Fatalf("document operations = %d, manifest routes = %d", count, operations)
	}
}

// TestGenerateResolvesEveryReference keeps the generated client from pointing
// at a missing component.
func TestGenerateResolvesEveryReference(t *testing.T) {
	document := generatedDocument(t)
	var visit func(value any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok {
				resolveReference(t, document, ref)
			}
			for _, item := range typed {
				visit(item)
			}
		case []any:
			for _, item := range typed {
				visit(item)
			}
		}
	}
	visit(document)
}

// TestGeneratedSchemasMatchContractTypes reflects over every schema named by
// the manifest, so the YAML cannot silently diverge from the DTO structs.
func TestGeneratedSchemasMatchContractTypes(t *testing.T) {
	document := generatedDocument(t)
	schemas := objectAt(t, objectAt(t, document, "components"), "schemas")
	for _, route := range contract.Routes {
		if route.Request != nil {
			assertComponentMatches(t, schemas, route.Request.Schema)
		}
		if route.Success.Schema == nil || route.Success.Binary {
			continue
		}
		valueType := reflect.TypeOf(route.Success.Schema)
		if valueType.Kind() == reflect.String && valueType.PkgPath() == "" {
			continue // text/plain probes carry no DTO
		}
		assertComponentMatches(t, schemas, route.Success.Schema)
	}

	audit := objectAt(t, schemas, "AuditEvent")
	id := objectAt(t, objectAt(t, audit, "properties"), "id")
	if id["format"] != "int64" {
		t.Errorf("AuditEvent.id format = %v, want int64", id["format"])
	}
}

func TestStaleIgnoresLineEndings(t *testing.T) {
	generated, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	crlf := bytes.ReplaceAll(generated, []byte("\n"), []byte("\r\n"))
	if stale(crlf, generated) {
		t.Fatal("CRLF checkout must not count as contract drift")
	}
	changed := append(append([]byte{}, generated...), []byte("stale\n")...)
	if !stale(changed, generated) {
		t.Fatal("content changes must be detected")
	}
}

func generatedDocument(t *testing.T) map[string]any {
	t.Helper()
	generated, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(generated, &document); err != nil {
		t.Fatalf("parse generated document: %v", err)
	}
	return document
}

func objectAt(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("key %q is missing", key)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("key %q is not an object: %T", key, value)
	}
	return object
}

func resolveReference(t *testing.T, document map[string]any, ref string) {
	t.Helper()
	name := strings.TrimPrefix(ref, "#/components/")
	if name == ref {
		t.Fatalf("unsupported reference %q", ref)
	}
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		t.Fatalf("malformed reference %q", ref)
	}
	section := objectAt(t, objectAt(t, document, "components"), parts[0])
	if _, ok := section[parts[1]]; !ok {
		t.Fatalf("reference %q points at a missing component", ref)
	}
}

func assertComponentMatches(t *testing.T, schemas map[string]any, value any) {
	t.Helper()
	valueType := reflect.TypeOf(value)
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if valueType.PkgPath() != reflect.TypeOf(contract.Error{}).PkgPath() {
		t.Fatalf("%s is not a contracts/httpapi type", valueType)
	}
	raw, ok := schemas[valueType.Name()]
	if !ok {
		t.Fatalf("component %q is missing", valueType.Name())
	}
	component, ok := raw.(map[string]any)
	if !ok || len(component) == 0 {
		t.Fatalf("component %q is empty", valueType.Name())
	}
	if valueType.Kind() != reflect.Struct {
		return
	}
	properties := map[string]any{}
	if _, ok := component["properties"]; ok {
		properties = objectAt(t, component, "properties")
	}
	want := structJSONNames(t, valueType)
	got := map[string]bool{}
	for name := range properties {
		got[name] = true
	}
	if missing, extra := setDifference(want, got), setDifference(got, want); len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: schema fields mismatch: missing %v, unexpected %v", valueType.Name(), missing, extra)
	}
	required := map[string]bool{}
	if rawRequired, ok := component["required"].([]any); ok {
		for _, item := range rawRequired {
			if name, ok := item.(string); ok {
				required[name] = true
			}
		}
	}
	for _, name := range requiredJSONNames(valueType) {
		if !required[name] {
			t.Errorf("%s: field %q is required by the struct but not by the schema", valueType.Name(), name)
		}
	}
}

func structJSONNames(t *testing.T, valueType reflect.Type) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for index := 0; index < valueType.NumField(); index++ {
		field := valueType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names[name] = true
	}
	return names
}

func requiredJSONNames(valueType reflect.Type) []string {
	var names []string
	for index := 0; index < valueType.NumField(); index++ {
		field := valueType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		if !hasTagOption(field.Tag.Get("binding"), "required") && !hasTagOption(field.Tag.Get("oa"), "required") {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if name == "-" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hasTagOption(tag, option string) bool {
	for _, item := range strings.Split(tag, ",") {
		if strings.TrimSpace(item) == option {
			return true
		}
	}
	return false
}

func setDifference(left, right map[string]bool) []string {
	var diff []string
	for name := range left {
		if !right[name] {
			diff = append(diff, name)
		}
	}
	sort.Strings(diff)
	return diff
}
