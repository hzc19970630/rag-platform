package knowledgeprojection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newWikiSecurityFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.Document{}, &models.DocumentVersion{}, &models.Chunk{},
		&models.WikiPage{}, &models.WikiPageVersion{}, &models.WikiPageSource{},
		&models.KnowledgeIndexRecord{}, &models.AuditLog{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb1", TenantID: "t1", OwnerID: "u1", Name: "KB", ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	if err := db.Create(&models.Document{
		ID: "d1", TenantID: "t1", KnowledgeBaseID: "kb1", OwnerID: "u1", Filename: "d.md",
		MimeType: "text/markdown", Status: ingestion.StatusEvidenceReady, LatestVersionID: "dv1",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "dv1", DocumentID: "d1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 1,
		ContentHash: "hash", SizeBytes: 1, SourceObjectKey: "src", Status: ingestion.StatusEvidenceReady,
		ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.Create(&models.Chunk{
		ID: "c1", TenantID: "t1", KnowledgeBaseID: "kb1", DocumentVersionID: "dv1", Ordinal: 0,
		Content: "chunk", ContentHash: "chunk-hash", Status: ingestion.StatusEvidenceReady,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	if err := db.Create(&models.WikiPage{
		ID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", PageType: "document", Slug: "page",
		Title: "Page", Status: PageDraft, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed wiki page: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv1", WikiPageID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 1,
		Content: "content", SourceHash: "source-hash", ReviewStatus: ReviewPending, ACLVersion: 1,
		CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed wiki version: %v", err)
	}
	if err := db.Create(&models.WikiPageSource{
		TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageVersionID: "wv1",
		DocumentID: "d1", DocumentVersionID: "dv1", ChunkID: "c1", SourcePath: "d.md",
	}).Error; err != nil {
		t.Fatalf("seed wiki source: %v", err)
	}
	service := NewService(Options{DB: db, Authz: fakeAuthorizer{}, Embedder: ingestion.NewLocalEmbedder(8)})
	return service, db
}

// Review, publish and rollback decide what becomes official knowledge, so an
// API key must never perform them even when it carries manage_members.
func TestWikiSensitiveActionsRejectAPIKeys(t *testing.T) {
	service, db := newWikiSecurityFixture(t)
	principal := authz.Principal{
		SubjectID: "u1", TenantID: "t1", CredentialType: "api_key", CredentialID: "key-1",
		Capabilities: []string{"knowledge_base:write", "knowledge_base:manage_members"},
	}
	ctx := context.Background()
	if _, err := service.Review(ctx, principal, "wp1", "wv1", ReviewInput{Action: "approve"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key review must be forbidden, got %v", err)
	}
	if _, err := service.Publish(ctx, principal, "wp1", "wv1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key publish must be forbidden, got %v", err)
	}
	if _, err := service.Rollback(ctx, principal, "wp1", RollbackInput{VersionID: "wv1"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key rollback must be forbidden, got %v", err)
	}
	var version models.WikiPageVersion
	if err := db.First(&version, "id = ?", "wv1").Error; err != nil {
		t.Fatalf("reload wiki version: %v", err)
	}
	if version.ReviewStatus != ReviewPending {
		t.Fatalf("forbidden action changed the review status to %s", version.ReviewStatus)
	}
}

// A rollback is not a publish: the audit trail must say which action ran.
func TestWikiRollbackAuditsItsOwnAction(t *testing.T) {
	service, db := newWikiSecurityFixture(t)
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Update("review_status", ReviewApproved).Error; err != nil {
		t.Fatalf("approve version: %v", err)
	}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.Rollback(context.Background(), principal, "wp1", RollbackInput{VersionID: "wv1"}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var audit models.AuditLog
	if err := db.First(&audit, "resource_id = ?", "wp1").Error; err != nil {
		t.Fatalf("rollback did not write an audit row: %v", err)
	}
	if !strings.Contains(audit.Reason, "wiki_rollback") {
		t.Fatalf("rollback audit must name the rollback action, got %q", audit.Reason)
	}
}
