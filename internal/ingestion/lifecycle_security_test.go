package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// seedRollbackState publishes ver-2 as the current version so activating ver-1
// is a real rollback instead of a no-op.
func seedRollbackState(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now()
	content := []byte("second version")
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-2", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		Version: 2, ContentHash: contentHash(content), SizeBytes: int64(len(content)),
		SourceObjectKey: "source-key-2", Status: StatusEvidenceReady, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed second version: %v", err)
	}
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Update("latest_version_id", "ver-2").Error; err != nil {
		t.Fatalf("point document at second version: %v", err)
	}
	for _, versionID := range []string{"ver-1", "ver-2"} {
		if err := db.Create(&models.PipelineRun{
			ID: "pipe-" + versionID, PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentID: "doc-a", DocumentVersionID: versionID, RequestID: "req-" + versionID, TraceID: "trace-" + versionID,
			IdempotencyKey: "pipe-" + versionID, Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed pipeline %s: %v", versionID, err)
		}
	}
}

func markDocumentDeleted(t *testing.T, db *gorm.DB, documentID string) {
	t.Helper()
	now := time.Now()
	if err := db.Model(&models.Document{}).Where("id = ?", documentID).Updates(map[string]any{
		"status": StatusCancelled, "deleted_at": &now, "deleted_by": "user-a",
		"purge_after": now.Add(time.Hour), "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("soft delete document: %v", err)
	}
}

// Purging hard-deletes every derived row, so an API key must not reach it even
// when it carries the delete capability. Only a session principal may purge.
func TestPurgeRejectsAPIKey(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	markDocumentDeleted(t, db, "doc-a")
	principal := authz.Principal{
		SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "api_key", CredentialID: "key-1",
		Capabilities: []string{"knowledge_base:delete", "knowledge_base:manage_members"},
	}
	if err := service.Purge(context.Background(), principal, "doc-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key purge must be forbidden, got %v", err)
	}
	var documents int64
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Count(&documents).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if documents != 1 {
		t.Fatalf("forbidden purge removed the document")
	}
}

// Rolling back changes which version retrieval serves, so it is a session
// action too. An API key with write capability must not flip the active version.
func TestActivateVersionRejectsAPIKey(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	seedRollbackState(t, db)
	principal := authz.Principal{
		SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "api_key", CredentialID: "key-1",
		Capabilities: []string{"knowledge_base:write"},
	}
	if _, err := service.ActivateVersion(context.Background(), principal, "doc-a", "ver-1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key rollback must be forbidden, got %v", err)
	}
	var document models.Document
	if err := db.First(&document, "id = ?", "doc-a").Error; err != nil {
		t.Fatalf("reload document: %v", err)
	}
	if document.LatestVersionID != "ver-2" {
		t.Fatalf("forbidden rollback changed the active version to %s", document.LatestVersionID)
	}
}

// Both actions are destructive or visibility-changing, so the operator must be
// attributable afterwards even when the session is legitimate.
func TestSensitiveDocumentActionsWriteAudit(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	seedRollbackState(t, db)
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}

	if _, err := service.ActivateVersion(context.Background(), principal, "doc-a", "ver-1"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var rollback models.AuditLog
	if err := db.First(&rollback, "action = ? AND resource_id = ?", "rollback", "doc-a").Error; err != nil {
		t.Fatalf("rollback did not write an audit row: %v", err)
	}
	if rollback.ResourceType != "document" || rollback.ActorID != "user-a" || !rollback.Allowed {
		t.Fatalf("unexpected rollback audit: %+v", rollback)
	}

	markDocumentDeleted(t, db, "doc-a")
	if err := service.Purge(context.Background(), principal, "doc-a"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var purge models.AuditLog
	if err := db.First(&purge, "action = ? AND resource_id = ?", "purge", "doc-a").Error; err != nil {
		t.Fatalf("purge did not write an audit row: %v", err)
	}
	if purge.ResourceType != "document" || purge.ActorID != "user-a" || !purge.Allowed {
		t.Fatalf("unexpected purge audit: %+v", purge)
	}
}
