package presets

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuiltinCatalogLoadsGeneralPack(t *testing.T) {
	catalog, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("load builtin: %v", err)
	}
	manifest, err := catalog.Get("general-enterprise", "")
	if err != nil {
		t.Fatalf("get pack: %v", err)
	}
	if manifest.Vertical != "general" || manifest.Version != "1.0.0" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if len(catalog.List()) == 0 {
		t.Fatal("catalog must not be empty")
	}
}

func TestValidateRejectsUnknownConfigKey(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: SchemaVersion, Name: "bad-pack", Version: "1.0.0",
		DisplayName: "Bad", Vertical: "general",
		Retrieval: map[string]json.RawMessage{"typo_key": json.RawMessage(`1`)},
		Chunking:  map[string]json.RawMessage{"size": json.RawMessage(`800`)},
		Formats:   []string{"markdown"},
	}
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestDiffReportsRetrievalAndChunkingChanges(t *testing.T) {
	left := Manifest{
		Version: "1.0.0", Retrieval: map[string]json.RawMessage{"top_k": json.RawMessage(`8`)},
		Chunking: map[string]json.RawMessage{"size": json.RawMessage(`800`)}, Formats: []string{"markdown"},
	}
	right := Manifest{
		Version: "1.1.0", Retrieval: map[string]json.RawMessage{"top_k": json.RawMessage(`10`)},
		Chunking: map[string]json.RawMessage{"size": json.RawMessage(`1000`)}, Formats: []string{"markdown"},
	}
	changes := Diff(left, right)
	seen := map[string]bool{}
	for _, change := range changes {
		seen[change.Section+"."+change.Key] = true
	}
	for _, key := range []string{"retrieval.top_k", "chunking.size", "metadata.version"} {
		if !seen[key] {
			t.Fatalf("missing diff %s in %+v", key, changes)
		}
	}
}
