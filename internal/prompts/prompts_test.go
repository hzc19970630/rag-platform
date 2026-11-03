package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemplates(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write template %s: %v", name, err)
		}
	}
	return dir
}

func completeTemplates() map[string]string {
	files := map[string]string{}
	for _, id := range Required {
		files[id+".yaml"] = "id: " + id + "\ntemplate: |\n  body of " + id + "\n"
	}
	return files
}

func TestLoadRequiresEveryDocumentedTemplate(t *testing.T) {
	files := completeTemplates()
	delete(files, "fallback.yaml")
	if _, err := Load(writeTemplates(t, files)); err == nil {
		t.Fatalf("expected a missing required template to fail the load")
	}
}

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	files := completeTemplates()
	files["duplicate.yaml"] = "id: fallback\ntemplate: |\n  another body\n"
	if _, err := Load(writeTemplates(t, files)); err == nil {
		t.Fatalf("expected a duplicate template id to fail the load")
	}
}

func TestLoadRejectsEmptyTemplate(t *testing.T) {
	files := completeTemplates()
	files["conversation_title.yaml"] = "id: conversation_title\ntemplate: \"\"\n"
	if _, err := Load(writeTemplates(t, files)); err == nil {
		t.Fatalf("expected an empty template to fail the load")
	}
}

func TestLoadAndRender(t *testing.T) {
	registry, err := Load(writeTemplates(t, completeTemplates()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(registry.IDs()) != len(Required) {
		t.Fatalf("expected %d templates, got %v", len(Required), registry.IDs())
	}
	rendered, err := registry.Render(ContextTemplate, map[string]string{"evidence": "[1] alpha", "query": "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(rendered, "{{") {
		t.Fatalf("placeholders were not substituted: %q", rendered)
	}
	if _, err := registry.Render("missing_template", nil); err == nil {
		t.Fatalf("expected an unknown template id to fail")
	}
}

func TestRenderLeavesUnknownPlaceholders(t *testing.T) {
	registry, err := Load(writeTemplates(t, completeTemplates()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rendered, err := registry.Render(SystemPrompt, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "body of system_prompt") {
		t.Fatalf("unexpected render result %q", rendered)
	}
}

func TestRepositoryTemplatesLoad(t *testing.T) {
	// The shipped templates must satisfy the same contract the API enforces at
	// startup, otherwise a deployment would fail on boot.
	registry, err := Load(filepath.Join("..", "..", "config", "prompt_templates"))
	if err != nil {
		t.Fatalf("shipped prompt templates must load: %v", err)
	}
	if len(registry.IDs()) != len(Required) {
		t.Fatalf("shipped templates: got %v", registry.IDs())
	}
}
