package tenantconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type allowOperator struct{}

func (allowOperator) RequireTenantOperator(context.Context, authz.Principal) error { return nil }

func openTenantConfigDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Tenant{}, &models.KnowledgeBase{}, &models.AuditLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&models.Tenant{ID: "tenant-1", Name: "one"}).Error; err != nil {
		t.Fatalf("seed tenant 1: %v", err)
	}
	if err := db.Create(&models.Tenant{ID: "tenant-2", Name: "two"}).Error; err != nil {
		t.Fatalf("seed tenant 2: %v", err)
	}
	return db
}

func testConfig() config.Config {
	return config.Config{
		Chunk: config.ChunkConfig{Size: 900, Overlap: 120, MinSize: 0, Markers: []string{"\n\n", "\n"}},
		Retrieval: config.RetrievalConfig{
			TopK: 8, EmbeddingTopK: 20, KeywordTopK: 20, VectorThreshold: 0.2,
			RerankTopK: 8, RerankThreshold: -10, VectorOversample: 1,
			VectorIterativeScan: true, ContextTokenLimit: 3000, AdjacentExpansion: 1,
			MaxAdjacentChunks: 2, RRFK: 60, ExpansionScoreFactor: 0.5,
		},
	}
}

func testDeployment() Deployment {
	cfg := testConfig()
	return Deployment{
		Retrieval: configschema.NewTier(config.RetrievalValues(cfg.Retrieval), map[string]bool{}),
		Chunking:  configschema.NewTier(config.ChunkingValues(cfg.Chunk), map[string]bool{}),
		Chunk: ChunkPolicy{
			Size: cfg.Chunk.Size, Overlap: cfg.Chunk.Overlap, MinSize: cfg.Chunk.MinSize, Markers: cfg.Chunk.Markers,
		},
	}
}

func rawValues(t *testing.T, values map[string]any) map[string]json.RawMessage {
	t.Helper()
	result := map[string]json.RawMessage{}
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode %s: %v", key, err)
		}
		result[key] = encoded
	}
	return result
}

func TestPutResolvesTenantTier(t *testing.T) {
	db := openTenantConfigDB(t)
	service := NewService(db, allowOperator{}, testDeployment())
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1", RequestID: "req-1"}

	view, err := service.Put(context.Background(), principal, Input{
		Retrieval: rawValues(t, map[string]any{"top_k": 5}),
		Chunking:  rawValues(t, map[string]any{"size": 512}),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if view.Retrieval.States["top_k"].Source != configschema.SourceTenant {
		t.Fatalf("top_k source = %+v", view.Retrieval.States["top_k"])
	}
	if string(view.Retrieval.Effective["top_k"]) != "5" {
		t.Fatalf("top_k effective = %s", view.Retrieval.Effective["top_k"])
	}
	if view.Retrieval.States["keyword_top_k"].Source != configschema.SourceDefault {
		t.Fatalf("keyword_top_k source = %+v", view.Retrieval.States["keyword_top_k"])
	}
	if string(view.Retrieval.States["keyword_top_k"].Default) != "20" {
		t.Fatalf("keyword_top_k default = %s", view.Retrieval.States["keyword_top_k"].Default)
	}
	if view.Chunking.States["size"].ApplyMode != configschema.ApplyNextIngest {
		t.Fatalf("chunking apply mode = %q", view.Chunking.States["size"].ApplyMode)
	}

	raw, err := service.TenantRetrievalOverride(context.Background(), "tenant-1")
	if err != nil || !strings.Contains(raw, `"top_k":5`) {
		t.Fatalf("tenant override = %q, err=%v", raw, err)
	}
	chunking, err := service.ChunkingForTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("chunking: %v", err)
	}
	if chunking.Size != 512 || chunking.Overlap != 120 {
		t.Fatalf("chunking = %+v", chunking)
	}
}

func TestPutRejectsUnknownAndOutOfRange(t *testing.T) {
	db := openTenantConfigDB(t)
	service := NewService(db, allowOperator{}, testDeployment())
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}

	if _, err := service.Put(context.Background(), principal, Input{Retrieval: rawValues(t, map[string]any{"top_k": 900})}); err == nil {
		t.Fatal("expected range error")
	}
	if _, err := service.Put(context.Background(), principal, Input{Retrieval: rawValues(t, map[string]any{"typo_key": 1})}); err == nil {
		t.Fatal("expected unknown key error")
	}
	if _, err := service.Put(context.Background(), principal, Input{Chunking: rawValues(t, map[string]any{"size": 100, "overlap": 100})}); err == nil {
		t.Fatal("expected chunking relation error")
	}
}

func TestPutIsTenantScoped(t *testing.T) {
	db := openTenantConfigDB(t)
	service := NewService(db, allowOperator{}, testDeployment())
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}

	if _, err := service.Put(context.Background(), principal, Input{
		Retrieval: rawValues(t, map[string]any{"top_k": 3}),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	var other models.Tenant
	if err := db.Where("id = ?", "tenant-2").Take(&other).Error; err != nil {
		t.Fatalf("load tenant 2: %v", err)
	}
	if strings.TrimSpace(other.RetrievalConfig) != "{}" {
		t.Fatalf("tenant 2 retrieval config leaked: %q", other.RetrievalConfig)
	}
	otherView, err := service.Get(context.Background(), authz.Principal{SubjectID: "owner", TenantID: "tenant-2"})
	if err != nil {
		t.Fatalf("get tenant 2: %v", err)
	}
	if otherView.Retrieval.States["top_k"].Source != configschema.SourceDefault {
		t.Fatalf("tenant 2 top_k source = %+v", otherView.Retrieval.States["top_k"])
	}
}

func TestPutWritesAuditRow(t *testing.T) {
	db := openTenantConfigDB(t)
	service := NewService(db, allowOperator{}, testDeployment())
	if _, err := service.Put(context.Background(), authz.Principal{SubjectID: "owner", TenantID: "tenant-1", RequestID: "req-9"}, Input{
		Retrieval: rawValues(t, map[string]any{"top_k": 4}),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	var audit models.AuditLog
	if err := db.Where("resource_type = ? AND resource_id = ?", "tenant_config", "tenant-1").Take(&audit).Error; err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if audit.Action != "update_tenant_config" || audit.RequestID != "req-9" || !strings.Contains(audit.Reason, "retrieval_after") {
		t.Fatalf("audit = %+v", audit)
	}
}
