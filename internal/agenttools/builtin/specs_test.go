package builtin

import (
	"testing"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
)

func TestSpecsAreValidAndUnique(t *testing.T) {
	specs := Specs()
	if len(specs) != 5 {
		t.Fatalf("specs = %d, want 5", len(specs))
	}
	if err := model.ValidateToolSpecs(specs); err != nil {
		t.Fatalf("invalid specs: %v", err)
	}
	names := map[string]bool{}
	for _, spec := range specs {
		if !spec.ReadOnly {
			t.Fatalf("tool %q must be read-only", spec.Name)
		}
		names[spec.Name] = true
	}
	for _, name := range []string{SearchKnowledge, ListDocuments, ListTables, TableSchema, TableQuery} {
		if !names[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
}
