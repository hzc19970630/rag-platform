package views

import (
	"encoding/json"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/knowledgebase"
	"github.com/hzc19970630/rag-platform/internal/presets"
)

func TestPresetViewsExposeSourceAndManifest(t *testing.T) {
	entries := []presets.Entry{
		{Source: presets.SourceBuiltin, Manifest: presets.Manifest{
			Name: "general-enterprise", Version: "1.0.0", DisplayName: "General", Vertical: "general", Formats: []string{"markdown"},
		}},
		{Source: presets.SourceTenant, Manifest: presets.Manifest{Name: "team-pack", Version: "1.0.0"}},
	}
	list := PresetList(entries)
	if len(list.Items) != 2 {
		t.Fatalf("items = %d", len(list.Items))
	}
	if list.Items[0].Name != "general-enterprise" || list.Items[0].Source != presets.SourceBuiltin {
		t.Fatalf("builtin item = %+v", list.Items[0])
	}
	if list.Items[1].Source != presets.SourceTenant {
		t.Fatalf("tenant item = %+v", list.Items[1])
	}
	if empty := PresetList(nil); empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty list must be an empty JSON array, got %#v", empty.Items)
	}

	detail := PresetDetail(presets.Manifest{Name: "team-pack", Version: "2.0.0"}, presets.SourceTenant)
	if detail.Source != presets.SourceTenant {
		t.Fatalf("detail source = %q", detail.Source)
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(detail.Manifest, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Name != "team-pack" || manifest.Version != "2.0.0" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestKnowledgeBaseConfigurationKeepsPresetBinding(t *testing.T) {
	view := KnowledgeBaseConfiguration(knowledgebase.KnowledgeBaseConfigurationV1{
		PresetName: "team-pack", PresetVersion: "1.0.0",
	})
	if view.PresetName != "team-pack" || view.PresetVersion != "1.0.0" {
		t.Fatalf("preset binding lost: %+v", view)
	}
	raw, err := json.Marshal(KnowledgeBaseConfiguration(knowledgebase.KnowledgeBaseConfigurationV1{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := fields["presetName"]; ok {
		t.Fatalf("empty preset name must stay omitted: %s", raw)
	}
}
