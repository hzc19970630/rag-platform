// Package prompts loads external prompt templates and validates template id
// references. Prompt wording is configuration, not code: changing it is a
// config release, not an image rebuild, and Go code must not carry prompt
// literals.
package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	SystemPrompt      = "system_prompt"
	ContextTemplate   = "context_template"
	Fallback          = "fallback"
	ConversationTitle = "conversation_title"
	// QueryRewrite turns a follow-up question into a standalone retrieval query.
	QueryRewrite = "query_rewrite"
	// QueryRewriteSystem is the system instruction for the rewriter.
	QueryRewriteSystem = "query_rewrite_system"
	// WikiGeneration drives Pipeline B page generation. It is a separate
	// template from the answer prompt so page wording and citation rules do not
	// constrain online generation.
	WikiGeneration = "wiki_generation"
	// RelationExtraction drives Relation Projection. Its output is the
	// RelationProjectionPackageV1 JSON object, never free-form prose.
	RelationExtraction = "relation_extraction"
)

// Required lists the template ids that must exist at startup. A missing or
// duplicated id fails startup rather than silently degrading a prompt to one
// written in code.
var Required = []string{
	SystemPrompt,
	ContextTemplate,
	Fallback,
	ConversationTitle,
	QueryRewrite,
	QueryRewriteSystem,
	WikiGeneration,
	RelationExtraction,
}

type template struct {
	ID       string `yaml:"id"`
	Template string `yaml:"template"`
}

// Registry holds validated templates by id.
type Registry struct {
	templates map[string]string
}

// Load reads every *.yaml file in dir, validates that ids are present, unique
// and complete, and returns the registry.
func Load(dir string) (*Registry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("prompt template directory is required")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read prompt template directory %s: %w", dir, err)
	}
	registry := &Registry{templates: map[string]string{}}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read prompt template %s: %w", entry.Name(), err)
		}
		var parsed template
		if err := yaml.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("parse prompt template %s: %w", entry.Name(), err)
		}
		id := strings.TrimSpace(parsed.ID)
		if id == "" {
			return nil, fmt.Errorf("prompt template %s is missing an id", entry.Name())
		}
		if strings.TrimSpace(parsed.Template) == "" {
			return nil, fmt.Errorf("prompt template %s has an empty template", entry.Name())
		}
		if _, exists := registry.templates[id]; exists {
			return nil, fmt.Errorf("duplicate prompt template id %q", id)
		}
		registry.templates[id] = parsed.Template
	}
	for _, id := range Required {
		if _, exists := registry.templates[id]; !exists {
			return nil, fmt.Errorf("missing required prompt template %q", id)
		}
	}
	return registry, nil
}

// IDs returns the loaded template ids in sorted order.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.templates))
	for id := range r.templates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Render substitutes {{name}} placeholders with the supplied values. Unknown
// placeholders are left untouched so a template can reference a variable that
// only some call sites provide.
func (r *Registry) Render(id string, values map[string]string) (string, error) {
	raw, ok := r.templates[id]
	if !ok {
		return "", fmt.Errorf("unknown prompt template %q", id)
	}
	rendered := raw
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	// Sort so output is deterministic when several placeholders are replaced.
	sort.Strings(names)
	for _, name := range names {
		rendered = strings.ReplaceAll(rendered, "{{"+name+"}}", values[name])
	}
	return strings.TrimRight(rendered, "\n"), nil
}

// Raw returns the template body without substitution.
func (r *Registry) Raw(id string) (string, bool) {
	raw, ok := r.templates[id]
	return raw, ok
}
