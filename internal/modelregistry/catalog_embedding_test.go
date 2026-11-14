package modelregistry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func embeddingRegistry(t *testing.T) *Registry {
	t.Helper()
	db := openRegistryTestDB(t)
	return newTestRegistry(t, db, Options{
		Now: time.Now,
		Models: []Model{
			{Name: "embed-8", Provider: "remote", Version: "v1", Dimension: 8, Capabilities: []model.Capability{model.CapabilityEmbedding}},
			{Name: "chat-a", Provider: "remote", Version: "v1", Capabilities: []model.Capability{model.CapabilityChat}},
		},
		Defaults: map[model.Capability]string{model.CapabilityEmbedding: "embed-8", model.CapabilityChat: "chat-a"},
	})
}

func TestResolveEmbeddingModelAndSpace(t *testing.T) {
	registry := embeddingRegistry(t)
	ctx := context.Background()
	binding, err := registry.ResolveEmbeddingModel(ctx, "tenant-1", "")
	if err != nil || binding.Model != "embed-8" || binding.Dimension != 8 {
		t.Fatalf("ResolveEmbeddingModel default = %+v, %v", binding, err)
	}
	space, err := registry.ResolveEmbeddingModelSpace(ctx, "tenant-1", "embed-8")
	if err != nil || !space.Deployment || space.Binding.Dimension != 8 {
		t.Fatalf("ResolveEmbeddingModelSpace = %+v, %v", space, err)
	}
	if _, err := registry.ResolveEmbeddingModel(ctx, "tenant-1", "unknown"); err == nil {
		t.Fatal("unknown embedding model must fail")
	}
	if _, err := registry.ResolveEmbeddingModel(ctx, "tenant-1", "chat-a"); err == nil {
		t.Fatal("chat model must not resolve as embedding")
	}
}

func TestTestDeploymentCapability(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{
		Now: time.Now,
		Models: []Model{
			{Name: "chat-ok", Provider: "remote", Version: "v1", Capabilities: []model.Capability{model.CapabilityChat}, Probe: func(context.Context) error { return nil }},
			{Name: "chat-bad", Provider: "remote", Version: "v1", Capabilities: []model.Capability{model.CapabilityRerank}, Probe: func(context.Context) error { return errors.New("probe down") }},
		},
	})
	result, err := registry.TestDeploymentCapability(context.Background(), model.CapabilityChat)
	if err != nil || !result.Healthy || result.CheckedAt.IsZero() {
		t.Fatalf("healthy capability = %+v, %v", result, err)
	}
	result, err = registry.TestDeploymentCapability(context.Background(), model.CapabilityRerank)
	if err != nil || result.Healthy || result.Error != "probe down" {
		t.Fatalf("unhealthy capability = %+v, %v", result, err)
	}
	if _, err := registry.TestDeploymentCapability(context.Background(), model.CapabilityEmbedding); err == nil {
		t.Fatal("unsupported capability must fail")
	}
}

func TestListCatalogMergesTenantEntries(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{
		Now: time.Now,
		Models: []Model{
			{Name: "chat-a", Provider: "remote", Version: "v1", Capabilities: []model.Capability{model.CapabilityChat}},
		},
	})
	now := time.Now().UTC()
	if err := db.Create(&models.ModelProvider{
		ID: "mp-1", TenantID: "tenant-1", Name: "tenant-provider", Kind: ProviderKindOpenAICompatible,
		BaseURL: "https://example.com/v1", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := db.Create(&models.ModelCatalogEntry{
		ID: "me-1", TenantID: "tenant-1", ProviderID: "mp-1", Model: "tenant-chat",
		DisplayName: "Tenant Chat", Capability: string(model.CapabilityChat), Version: "v1",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog entry: %v", err)
	}
	catalog, err := registry.ListCatalog(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListCatalog: %v", err)
	}
	sources := map[string]bool{}
	modelsByName := map[string]bool{}
	for _, entry := range catalog {
		sources[entry.Source] = true
		modelsByName[entry.Model] = true
	}
	if !sources["deployment"] || !sources["provider"] || !modelsByName["chat-a"] || !modelsByName["tenant-chat"] {
		t.Fatalf("catalog = %+v", catalog)
	}
}
