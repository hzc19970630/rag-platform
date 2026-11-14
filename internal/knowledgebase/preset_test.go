package knowledgebase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/presets"
)

type fakePresetCatalog struct {
	manifest presets.Manifest
	err      error
	gotName  string
	gotVer   string
}

func (c *fakePresetCatalog) Get(_ context.Context, _ string, name, version string) (presets.Manifest, error) {
	c.gotName = name
	c.gotVer = version
	return c.manifest, c.err
}

func TestCreateAppliesPresetDefaults(t *testing.T) {
	service, _ := newKBFixture(t)
	catalog := &fakePresetCatalog{manifest: presets.Manifest{
		Name:    "team-pack",
		Version: "1.0.0",
		Retrieval: map[string]json.RawMessage{
			"top_k": json.RawMessage(`5`),
		},
		Chunking: map[string]json.RawMessage{
			"size":    json.RawMessage(`400`),
			"overlap": json.RawMessage(`40`),
		},
	}}
	service.SetPresetCatalog(catalog)

	created, err := service.Create(context.Background(), ownerA(), CreateInput{
		Name: "Preset KB", PresetName: "team-pack", PresetVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("create with preset: %v", err)
	}
	if catalog.gotName != "team-pack" || catalog.gotVer != "1.0.0" {
		t.Fatalf("catalog lookup = %q@%q", catalog.gotName, catalog.gotVer)
	}
	if created.PresetName != "team-pack" || created.PresetVersion != "1.0.0" {
		t.Fatalf("preset binding = %q@%q", created.PresetName, created.PresetVersion)
	}
	var retrieval map[string]any
	if err := json.Unmarshal([]byte(created.RetrievalConfig), &retrieval); err != nil || retrieval["top_k"] != float64(5) {
		t.Fatalf("retrieval config = %s, err=%v", created.RetrievalConfig, err)
	}
	var chunking map[string]any
	if err := json.Unmarshal([]byte(created.ChunkingConfig), &chunking); err != nil || chunking["size"] != float64(400) || chunking["overlap"] != float64(40) {
		t.Fatalf("chunking config = %s, err=%v", created.ChunkingConfig, err)
	}

	withoutPreset, err := service.Create(context.Background(), ownerA(), CreateInput{Name: "Plain KB"})
	if err != nil {
		t.Fatalf("create without preset: %v", err)
	}
	if withoutPreset.PresetName != "" || withoutPreset.PresetVersion != "" {
		t.Fatalf("plain knowledge base must not inherit a preset binding: %+v", withoutPreset)
	}
	if withoutPreset.ChunkingConfig != "{}" || withoutPreset.RetrievalConfig != "{}" {
		t.Fatalf("plain knowledge base must keep empty config defaults: %+v", withoutPreset)
	}
}

func TestCreatePresetLookupErrors(t *testing.T) {
	service, _ := newKBFixture(t)
	if _, err := service.Create(context.Background(), ownerA(), CreateInput{Name: "No Catalog", PresetName: "team-pack"}); err == nil {
		t.Fatal("missing preset catalog must be rejected")
	} else if _, ok := apperror.AsValidation(err); !ok {
		t.Fatalf("missing catalog = %v, want validation error", err)
	}
	service.SetPresetCatalog(&fakePresetCatalog{err: apperror.Invalidf("unknown preset %q", "missing-pack")})
	if _, err := service.Create(context.Background(), ownerA(), CreateInput{Name: "Unknown", PresetName: "missing-pack"}); err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("unknown preset = %v", err)
	}
}
