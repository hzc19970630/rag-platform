package knowledgebase

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBuildKnowledgeBaseConfiguration(t *testing.T) {
	defaults := config.RetrievalConfig{
		TopK: 10, EmbeddingTopK: 20, KeywordTopK: 20, VectorThreshold: 0.6, KeywordThreshold: 0,
		RerankEnabled: false, RerankTopK: 10, RerankThreshold: 0, ContextTokenLimit: 3000,
		AdjacentExpansion: 0, MaxAdjacentChunks: 2, RRFK: 60, ExpansionScoreFactor: 0.5,
	}
	readOnly := ReadOnlySystemConfiguration(config.Config{
		ParserVersion:    "docreader-test",
		SupportedFormats: []string{"pdf-text", "markdown"},
		Embedding:        config.EmbeddingConfig{Source: "remote", Model: "embed", Dimension: 1024, APIKey: "secret"},
		Chunk:            config.ChunkConfig{Size: 800, Overlap: 120, Markers: []string{"\n\n", "."}, MinSize: 10},
		Chat:             config.ChatConfig{Source: "remote", Model: "chat", APIKey: "secret"},
		Rerank:           config.RerankConfig{Source: "none"},
	})
	topK := 5
	override := retrieval.ParamsOverride{TopK: &topK}
	encodedOverride, _ := json.Marshal(override)
	knowledgeBase := models.KnowledgeBase{
		ID: "kb-1", ACLVersion: 7, RetrievalConfig: string(encodedOverride),
		IndexingStrategy: `{"vector_enabled":true,"keyword_enabled":false,"wiki_enabled":true,"graph_enabled":false,"required_capabilities":["evidence"]}`,
	}

	view, err := BuildKnowledgeBaseConfiguration(knowledgeBase, defaults, readOnly)
	if err != nil {
		t.Fatalf("build configuration: %v", err)
	}
	if view.SchemaVersion != SchemaKnowledgeBaseConfigurationV1 || view.KnowledgeBaseID != "kb-1" || view.ACLVersion != 7 {
		t.Fatalf("unexpected envelope: %+v", view)
	}
	if view.Retrieval.GlobalDefaults.TopK != 10 || view.Retrieval.Effective.TopK != 5 {
		t.Fatalf("global/effective top_k not resolved: %+v", view.Retrieval)
	}
	if view.Retrieval.Sources["top_k"] != retrievalSourceKnowledgeBase || view.Retrieval.Sources["keyword_top_k"] != retrievalSourceGlobal {
		t.Fatalf("unexpected sources: %+v", view.Retrieval.Sources)
	}
	if view.IndexingStrategy.KeywordEnabled || !view.IndexingStrategy.WikiEnabled {
		t.Fatalf("indexing strategy not preserved: %+v", view.IndexingStrategy)
	}
	if view.ReadOnly.Parser.Version != "docreader-test" || len(view.ReadOnly.Parser.SupportedFormats) != 2 {
		t.Fatalf("unexpected parser view: %+v", view.ReadOnly.Parser)
	}
	if view.ReadOnly.Chunking.SplitMarkers != "\n\n,." || view.ReadOnly.Chunking.MinSize != 10 {
		t.Fatalf("unexpected chunking view: %+v", view.ReadOnly.Chunking)
	}
}

func TestReadOnlySystemConfigurationFallsBackToDefaultParserVersion(t *testing.T) {
	view := ReadOnlySystemConfiguration(config.Config{})
	if view.Parser.Version != config.DefaultParserVersion {
		t.Fatalf("expected default parser version, got %q", view.Parser.Version)
	}
}

func TestReplaceRetrievalConfigWholeReplacementAndClear(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "test-embedding", Dimension: 3})
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1", RequestID: "req-1"}
	defaults := config.RetrievalConfig{TopK: 10, EmbeddingTopK: 20, KeywordTopK: 20, VectorThreshold: 0.6, ContextTokenLimit: 3000, RerankTopK: 10, MaxAdjacentChunks: 2, RRFK: 60, ExpansionScoreFactor: 0.5, RerankThreshold: 0, AdjacentExpansion: 0, KeywordThreshold: 0, RerankEnabled: false}
	readOnly := ReadOnlySystemConfiguration(config.Config{})

	topK := 4
	threshold := 0.3
	row, err := service.ReplaceRetrievalConfig(context.Background(), principal, "kb-1", retrieval.ParamsOverride{TopK: &topK, VectorThreshold: &threshold})
	if err != nil {
		t.Fatalf("replace retrieval config: %v", err)
	}
	if row.ACLVersion != 3 {
		t.Fatalf("retrieval config must not bump acl_version, got %d", row.ACLVersion)
	}
	view, err := BuildKnowledgeBaseConfiguration(row, defaults, readOnly)
	if err != nil {
		t.Fatalf("build after replace: %v", err)
	}
	if view.Retrieval.Effective.TopK != 4 || view.Retrieval.Effective.VectorThreshold != 0.3 {
		t.Fatalf("unexpected effective values: %+v", view.Retrieval.Effective)
	}
	if view.Retrieval.Sources["top_k"] != retrievalSourceKnowledgeBase || view.Retrieval.Sources["keyword_top_k"] != retrievalSourceGlobal {
		t.Fatalf("unexpected sources: %+v", view.Retrieval.Sources)
	}

	row, err = service.ReplaceRetrievalConfig(context.Background(), principal, "kb-1", retrieval.ParamsOverride{})
	if err != nil {
		t.Fatalf("clear retrieval config: %v", err)
	}
	if strings.TrimSpace(row.RetrievalConfig) != "{}" {
		t.Fatalf("expected cleared config {}, got %q", row.RetrievalConfig)
	}
	view, err = BuildKnowledgeBaseConfiguration(row, defaults, readOnly)
	if err != nil {
		t.Fatalf("build after clear: %v", err)
	}
	if view.Retrieval.Effective.TopK != 10 || view.Retrieval.Sources["top_k"] != retrievalSourceGlobal {
		t.Fatalf("clear must fall back to global defaults: %+v", view.Retrieval)
	}

	var auditCount int64
	if err := db.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ?", "retrieval_config", "kb-1").Count(&auditCount).Error; err != nil {
		t.Fatalf("count audit logs: %v", err)
	}
	if auditCount != 2 {
		t.Fatalf("expected two retrieval config audits, got %d", auditCount)
	}
}

func TestReplaceRetrievalConfigPermissionsAndValidation(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "test-embedding", Dimension: 3})
	viewer := authz.Principal{SubjectID: "viewer", TenantID: "tenant-1"}
	defaults := config.RetrievalConfig{TopK: 10, EmbeddingTopK: 20, KeywordTopK: 20, VectorThreshold: 0.6, ContextTokenLimit: 3000, RerankTopK: 10, MaxAdjacentChunks: 2, RRFK: 60, ExpansionScoreFactor: 0.5}
	if _, err := service.Configuration(context.Background(), viewer, "kb-1", defaults, ReadOnlySystemConfiguration(config.Config{})); err != nil {
		t.Fatalf("viewer must be able to read configuration: %v", err)
	}
	if _, err := service.ReplaceRetrievalConfig(context.Background(), viewer, "kb-1", retrieval.ParamsOverride{}); err != apperror.ErrForbidden {
		t.Fatalf("viewer write must return forbidden, got %v", err)
	}

	editor := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}
	tooLarge := 201
	if _, err := service.ReplaceRetrievalConfig(context.Background(), editor, "kb-1", retrieval.ParamsOverride{TopK: &tooLarge}); err == nil || !strings.Contains(err.Error(), "must be between") {
		t.Fatalf("expected range validation error, got %v", err)
	}
}

type recordingProjection struct {
	invalidated   int
	wikiRebuilds  int
	graphRebuilds int
}

func (r *recordingProjection) InvalidateKnowledgeBase(context.Context, string, string, string) error {
	r.invalidated++
	return nil
}

func (r *recordingProjection) RebuildKnowledgeBase(context.Context, string, string) (int, error) {
	r.wikiRebuilds++
	return 1, nil
}

func (r *recordingProjection) RebuildRelations(context.Context, string, string) (int, error) {
	r.graphRebuilds++
	return 1, nil
}

func TestIndexingStrategyOnlyInvalidatesProjectionForProjectionSwitches(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "test-embedding", Dimension: 3})
	projection := &recordingProjection{}
	service.SetProjectionLifecycle(projection)
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}
	vectorOff := retrieval.IndexingStrategy{VectorEnabled: false, KeywordEnabled: true, RequiredCapabilities: []string{retrieval.CapabilityEvidence}}
	row, err := service.Update(context.Background(), principal, "kb-1", UpdateInput{IndexingStrategy: &vectorOff})
	if err != nil {
		t.Fatalf("update vector switch: %v", err)
	}
	if row.ACLVersion != 3 || projection.invalidated != 0 {
		t.Fatalf("vector/keyword-only change must keep acl and projections, got acl=%d invalidations=%d", row.ACLVersion, projection.invalidated)
	}
	wikiOn := retrieval.IndexingStrategy{VectorEnabled: false, KeywordEnabled: true, WikiEnabled: true, RequiredCapabilities: []string{retrieval.CapabilityEvidence}}
	row, err = service.Update(context.Background(), principal, "kb-1", UpdateInput{IndexingStrategy: &wikiOn})
	if err != nil {
		t.Fatalf("enable wiki: %v", err)
	}
	if row.ACLVersion != 4 || projection.invalidated != 1 || projection.wikiRebuilds != 1 {
		t.Fatalf("wiki switch must bump acl and rebuild, got acl=%d invalidations=%d rebuilds=%d", row.ACLVersion, projection.invalidated, projection.wikiRebuilds)
	}
	wikiOff := retrieval.IndexingStrategy{VectorEnabled: false, KeywordEnabled: true, RequiredCapabilities: []string{retrieval.CapabilityEvidence}}
	row, err = service.Update(context.Background(), principal, "kb-1", UpdateInput{IndexingStrategy: &wikiOff})
	if err != nil {
		t.Fatalf("disable wiki: %v", err)
	}
	if row.ACLVersion != 5 || projection.invalidated != 2 || projection.wikiRebuilds != 1 {
		t.Fatalf("disabling wiki must invalidate without a rebuild, got acl=%d invalidations=%d rebuilds=%d", row.ACLVersion, projection.invalidated, projection.wikiRebuilds)
	}
	var auditCount int64
	if err := db.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ?", "indexing_strategy", "kb-1").Count(&auditCount).Error; err != nil {
		t.Fatalf("count strategy audits: %v", err)
	}
	if auditCount != 3 {
		t.Fatalf("expected three indexing strategy audits, got %d", auditCount)
	}
}

func openConfigurationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// The Casbin adapter's SavePolicy keeps a transaction open while truncating
	// the policy table through a second connection, so this database needs a
	// real file and more than one pooled connection.
	dsn := "file:" + filepath.Join(t.TempDir(), "configuration.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	if err := db.AutoMigrate(&models.Membership{}, &models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.Document{}, &models.AuthorizationRevision{}, &models.AuditLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	now := time.Now().UTC()
	knowledgeBase := models.KnowledgeBase{
		ID: "kb-1", TenantID: "tenant-1", OwnerID: "owner", Name: "Configuration KB", ACLVersion: 3,
		RetrievalConfig: "{}", IndexingStrategy: `{"vector_enabled":true,"keyword_enabled":true,"wiki_enabled":false,"graph_enabled":false,"required_capabilities":["evidence"]}`,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&knowledgeBase).Error; err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	return db
}

func seedAuthorization(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	rows := []any{
		&models.Membership{TenantID: "tenant-1", UserID: "owner", Role: "owner", CreatedAt: now, UpdatedAt: now},
		&models.Membership{TenantID: "tenant-1", UserID: "viewer", Role: "viewer", CreatedAt: now, UpdatedAt: now},
		&models.Membership{TenantID: "tenant-1", UserID: "editor", Role: "editor", CreatedAt: now, UpdatedAt: now},
		&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-1", UserID: "owner", Role: "owner", CreatedAt: now, UpdatedAt: now},
		&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-1", UserID: "viewer", Role: "viewer", CreatedAt: now, UpdatedAt: now},
		&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-1", UserID: "editor", Role: "editor", CreatedAt: now, UpdatedAt: now},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed authorization: %v", err)
		}
	}
}

func mustAuthorizer(t *testing.T, db *gorm.DB) *authz.Service {
	t.Helper()
	service, err := authz.NewService(db)
	if err != nil {
		t.Fatalf("create authorizer: %v", err)
	}
	return service
}
