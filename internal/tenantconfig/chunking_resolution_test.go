package tenantconfig

import (
	"context"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestChunkingForKnowledgeBaseOverlaysTenantPolicy(t *testing.T) {
	db := openTenantConfigDB(t)
	service := NewService(db, allowOperator{}, testDeployment())
	ctx := context.Background()
	if err := db.Model(&models.Tenant{}).Where("id = ?", "tenant-1").Update("chunking_config", `{"size":600}`).Error; err != nil {
		t.Fatalf("seed tenant chunking: %v", err)
	}
	rows := []models.KnowledgeBase{
		{ID: "kb-override", TenantID: "tenant-1", OwnerID: "owner", Name: "override", ChunkingConfig: `{"size":400,"overlap":40}`, ACLVersion: 1},
		{ID: "kb-empty", TenantID: "tenant-1", OwnerID: "owner", Name: "empty", ChunkingConfig: `{}`, ACLVersion: 1},
		{ID: "kb-bad-json", TenantID: "tenant-1", OwnerID: "owner", Name: "bad-json", ChunkingConfig: `{bad`, ACLVersion: 1},
		{ID: "kb-unknown-key", TenantID: "tenant-1", OwnerID: "owner", Name: "unknown", ChunkingConfig: `{"bogus":1}`, ACLVersion: 1},
		{ID: "kb-other", TenantID: "tenant-2", OwnerID: "owner", Name: "other", ChunkingConfig: `{"size":123}`, ACLVersion: 1},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed knowledge base %s: %v", row.ID, err)
		}
	}

	overridden, err := service.ChunkingForKnowledgeBase(ctx, "tenant-1", "kb-override")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if overridden.Size != 400 || overridden.Overlap != 40 {
		t.Fatalf("knowledge-base override not applied: %+v", overridden)
	}
	if len(overridden.Markers) != len(testDeployment().Chunk.Markers) {
		t.Fatalf("markers should inherit deployment defaults: %+v", overridden)
	}

	inherited, err := service.ChunkingForKnowledgeBase(ctx, "tenant-1", "kb-empty")
	if err != nil {
		t.Fatalf("empty override: %v", err)
	}
	if inherited.Size != 600 {
		t.Fatalf("tenant policy not inherited: %+v", inherited)
	}

	for _, scope := range []struct {
		name string
		kb   string
	}{
		{"missing knowledge base", "kb-missing"},
		{"empty knowledge base id", ""},
		{"other tenant knowledge base", "kb-other"},
	} {
		policy, err := service.ChunkingForKnowledgeBase(ctx, "tenant-1", scope.kb)
		if err != nil {
			t.Fatalf("%s: %v", scope.name, err)
		}
		if policy.Size != 600 {
			t.Fatalf("%s leaked a foreign override: %+v", scope.name, policy)
		}
	}

	if _, err := service.ChunkingForKnowledgeBase(ctx, "tenant-1", "kb-bad-json"); err == nil {
		t.Fatal("invalid chunking JSON must fail")
	}
	if _, err := service.ChunkingForKnowledgeBase(ctx, "tenant-1", "kb-unknown-key"); err == nil {
		t.Fatal("unknown chunking key must fail validation")
	}
}
