package configpreview

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeAuthz struct {
	allow bool
}

func (f fakeAuthz) RequireTenantOperator(context.Context, authz.Principal) error {
	if !f.allow {
		return apperror.ErrForbidden
	}
	return nil
}

func (f fakeAuthz) AuthorizeKnowledgeBase(context.Context, authz.Principal, string, string) (bool, error) {
	return f.allow, nil
}

func testDeployment() tenantconfig.Deployment {
	return tenantconfig.Deployment{
		Retrieval: configschema.NewTier(map[string]any{"top_k": 8}, map[string]bool{"top_k": true}),
		Chunking:  configschema.NewTier(map[string]any{"size": 900}, map[string]bool{"size": true}),
		Chunk:     tenantconfig.ChunkPolicy{Size: 900, Overlap: 120, Markers: []string{"\n\n"}},
	}
}

func openPreviewDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, name TEXT, quota TEXT, retrieval_config TEXT, chunking_config TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE knowledge_bases (id TEXT PRIMARY KEY, tenant_id TEXT, owner_id TEXT, name TEXT, description TEXT, embedding_model TEXT, embedding_dimension INTEGER, chat_model TEXT, chat_model_version TEXT, rerank_model TEXT, rerank_model_version TEXT, acl_version INTEGER, retrieval_config TEXT, indexing_strategy TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE documents (id TEXT PRIMARY KEY, tenant_id TEXT, knowledge_base_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE chunks (id TEXT PRIMARY KEY, tenant_id TEXT, knowledge_base_id TEXT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	if err := db.Create(&models.Tenant{ID: "tenant-1", Name: "one", RetrievalConfig: `{"top_k":8}`, ChunkingConfig: `{}`}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{ID: "kb-1", TenantID: "tenant-1", Name: "one", RetrievalConfig: `{}`, IndexingStrategy: `{}`}).Error; err != nil {
		t.Fatalf("seed kb1: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{ID: "kb-2", TenantID: "tenant-1", Name: "two", RetrievalConfig: `{"top_k":4}`, IndexingStrategy: `{}`}).Error; err != nil {
		t.Fatalf("seed kb2: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO documents (id, tenant_id, knowledge_base_id) VALUES ('d1','tenant-1','kb-1'),('d2','tenant-1','kb-1'),('d3','tenant-1','kb-2')`,
		`INSERT INTO chunks (id, tenant_id, knowledge_base_id) VALUES ('c1','tenant-1','kb-1'),('c2','tenant-1','kb-1'),('c3','tenant-1','kb-1'),('c4','tenant-1','kb-2')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed counts: %v", err)
		}
	}
	return db
}

func raw(t *testing.T, values map[string]any) map[string]json.RawMessage {
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

func service(t *testing.T, db *gorm.DB, allow bool) *Service {
	t.Helper()
	return NewService(Options{
		DB: db, Authz: fakeAuthz{allow: allow}, Deployment: testDeployment(),
		EmbeddingModel: "embed-a", EmbeddingDimension: 3,
	})
}

func TestTenantRetrievalPreviewRespectsKnowledgeBaseOverrides(t *testing.T) {
	db := openPreviewDB(t)
	preview, err := service(t, db, true).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeTenant, Retrieval: raw(t, map[string]any{"top_k": 6}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Impact != ImpactHot || preview.RebuildRequired || preview.RebuildOptional {
		t.Fatalf("preview summary = %+v", preview)
	}
	if len(preview.Changes) != 1 || preview.Changes[0].Key != "top_k" || string(preview.Changes[0].NewValue) != "6" {
		t.Fatalf("changes = %+v", preview.Changes)
	}
	if preview.Affected.KnowledgeBases != 1 || preview.Affected.Documents != 2 || preview.Affected.Chunks != 3 {
		t.Fatalf("affected = %+v", preview.Affected)
	}
	if len(preview.Actions) != 1 || preview.Actions[0] != ActionApplyNow {
		t.Fatalf("actions = %+v", preview.Actions)
	}
}

func TestTenantChunkingPreviewIsOptionalRebuild(t *testing.T) {
	db := openPreviewDB(t)
	preview, err := service(t, db, true).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeTenant, Chunking: raw(t, map[string]any{"size": 512}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Impact != ImpactNextIngest || !preview.RebuildOptional || preview.RebuildRequired {
		t.Fatalf("preview summary = %+v", preview)
	}
	if preview.Affected.KnowledgeBases != 2 || preview.Affected.Documents != 3 || preview.Affected.Chunks != 4 {
		t.Fatalf("affected = %+v", preview.Affected)
	}
	if preview.EstimatedDurationTier != "low" || preview.EstimatedCostTier != "low" {
		t.Fatalf("estimate = %s/%s", preview.EstimatedDurationTier, preview.EstimatedCostTier)
	}
}

func TestKnowledgeBasePreviewUsesKnowledgeBaseSource(t *testing.T) {
	db := openPreviewDB(t)
	preview, err := service(t, db, true).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeKnowledgeBase, KnowledgeBaseID: "kb-1", Retrieval: raw(t, map[string]any{"top_k": 4}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Changes) != 1 || preview.Changes[0].Source != configschema.SourceKnowledgeBase {
		t.Fatalf("changes = %+v", preview.Changes)
	}
	if preview.Affected.KnowledgeBases != 1 || preview.Affected.Documents != 2 || preview.Affected.Chunks != 3 {
		t.Fatalf("affected = %+v", preview.Affected)
	}
}

func TestEmbeddingChangeIsBlocked(t *testing.T) {
	db := openPreviewDB(t)
	dimension := 1024
	preview, err := service(t, db, true).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeKnowledgeBase, KnowledgeBaseID: "kb-1", EmbeddingDimension: &dimension,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.RebuildRequired || preview.Impact != ImpactRebuild {
		t.Fatalf("summary = %+v", preview)
	}
	if len(preview.Blockers) != 1 || preview.Blockers[0].Code != BlockerEmbeddingContract {
		t.Fatalf("blockers = %+v", preview.Blockers)
	}
	if len(preview.Actions) != 1 || preview.Actions[0] != ActionMigrateEmbedding {
		t.Fatalf("actions = %+v", preview.Actions)
	}
}

func TestPreviewRejectsUnknownKeyAndDeniedWriter(t *testing.T) {
	db := openPreviewDB(t)
	if _, err := service(t, db, true).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeTenant, Retrieval: raw(t, map[string]any{"typo_key": 1}),
	}); err == nil {
		t.Fatal("expected unknown key error")
	}
	if _, err := service(t, db, false).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeKnowledgeBase, KnowledgeBaseID: "kb-1", Retrieval: raw(t, map[string]any{"top_k": 4}),
	}); err != apperror.ErrForbidden {
		t.Fatalf("denied writer error = %v", err)
	}
}

func TestPreviewWithNoChanges(t *testing.T) {
	db := openPreviewDB(t)
	preview, err := service(t, db, true).Preview(context.Background(), authz.Principal{TenantID: "tenant-1"}, Input{
		Scope: ScopeTenant, Retrieval: raw(t, map[string]any{"top_k": 8}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Impact != ImpactNone || len(preview.Changes) != 0 || preview.Affected.KnowledgeBases != 0 {
		t.Fatalf("preview = %+v", preview)
	}
}
