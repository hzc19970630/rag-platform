package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"gorm.io/gorm"
)

type healthProbe interface {
	Probe(context.Context) error
}

// probeOf returns the adapter's liveness probe, or nil for offline adapters
// that have no remote dependency. A nil probe is registered as always healthy.
func probeOf(adapter any) func(context.Context) error {
	if prober, ok := adapter.(healthProbe); ok {
		return prober.Probe
	}
	return nil
}

// newModelRegistry describes the adapters this process actually built. Only a
// remote provider can serve extra model names; offline adapters ignore the
// requested model, so registering aliases for them would be misleading.
func newModelRegistry(db *gorm.DB, cfg config.Config, chatModel, rerankModel, embeddingModel string, chatProbe, rerankProbe, embeddingProbe func(context.Context) error) (*modelregistry.Registry, error) {
	entries := []modelregistry.Model{
		{
			Name: chatModel, Provider: cfg.Chat.Source, Version: "deployment",
			Capabilities: []model.Capability{model.CapabilityChat}, Probe: chatProbe,
		},
		{
			Name: rerankModel, Provider: cfg.Rerank.Source, Version: "deployment",
			Capabilities: []model.Capability{model.CapabilityRerank}, Probe: rerankProbe,
		},
		{
			Name: embeddingModel, Provider: cfg.Embedding.Source, Version: "deployment",
			Capabilities: []model.Capability{model.CapabilityEmbedding}, Probe: embeddingProbe,
		},
	}
	if cfg.Chat.Source == "remote" {
		extra, err := modelEntries(cfg.Chat.Models, model.CapabilityChat, cfg.Chat.Source, chatProbe)
		if err != nil {
			return nil, err
		}
		entries = append(entries, extra...)
	}
	if cfg.Rerank.Source == "remote" {
		extra, err := modelEntries(cfg.Rerank.Models, model.CapabilityRerank, cfg.Rerank.Source, rerankProbe)
		if err != nil {
			return nil, err
		}
		entries = append(entries, extra...)
	}
	entries = dedupeModels(entries)
	return modelregistry.New(db, modelregistry.Options{
		Models: entries,
		Defaults: map[model.Capability]string{
			model.CapabilityChat:      chatModel,
			model.CapabilityRerank:    rerankModel,
			model.CapabilityEmbedding: embeddingModel,
		},
		RequestsPerMinute: cfg.Limits.TenantModelRequestsPerMinute,
	})
}

// dedupeModels keeps the last entry for a name so an explicit name@version in
// CHAT_MODELS overrides the deployment-default version.
func dedupeModels(entries []modelregistry.Model) []modelregistry.Model {
	index := make(map[string]int, len(entries))
	result := make([]modelregistry.Model, 0, len(entries))
	for _, entry := range entries {
		if position, ok := index[entry.Name]; ok {
			result[position] = entry
			continue
		}
		index[entry.Name] = len(result)
		result = append(result, entry)
	}
	return result
}

func modelEntries(entries []string, capability model.Capability, provider string, probe func(context.Context) error) ([]modelregistry.Model, error) {
	result := make([]modelregistry.Model, 0, len(entries))
	for _, entry := range entries {
		name, version, hasVersion := strings.Cut(entry, "@")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("model registry: empty model name in %q", entry)
		}
		if !hasVersion {
			version = "deployment"
		}
		result = append(result, modelregistry.Model{
			Name: name, Provider: provider, Version: strings.TrimSpace(version),
			Capabilities: []model.Capability{capability}, Probe: probe,
		})
	}
	return result, nil
}
