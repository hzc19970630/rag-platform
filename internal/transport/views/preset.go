package views

import (
	"encoding/json"

	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/presets"
)

// PresetList maps the catalog to the preset management/list wire shape.
func PresetList(entries []presets.Entry) httpapi.PresetList {
	result := make([]httpapi.Preset, 0, len(entries))
	for _, entry := range entries {
		item := entry.Manifest
		result = append(result, httpapi.Preset{
			Name: item.Name, Version: item.Version, DisplayName: item.DisplayName,
			Description: item.Description, Vertical: item.Vertical, Formats: item.Formats, Source: entry.Source,
		})
	}
	return httpapi.PresetList{Items: result}
}

// PresetDetail returns the editable manifest plus its origin.
func PresetDetail(manifest presets.Manifest, source string) httpapi.PresetDetail {
	raw, _ := json.Marshal(manifest)
	return httpapi.PresetDetail{Source: source, Manifest: raw}
}
