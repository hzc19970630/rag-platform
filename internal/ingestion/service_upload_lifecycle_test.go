package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func ownerPrincipal() authz.Principal {
	return authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
}

func TestUploadValidationAndAuthorization(t *testing.T) {
	service, _, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()

	tests := []struct {
		name  string
		input UploadInput
		want  string
	}{
		{name: "empty content", input: UploadInput{KnowledgeBaseID: "kb-a", Filename: "a.txt"}, want: "file is required"},
		{name: "empty filename", input: UploadInput{KnowledgeBaseID: "kb-a", Content: []byte("x")}, want: "filename is required"},
		{name: "unsupported mime", input: UploadInput{KnowledgeBaseID: "kb-a", Filename: "a.exe", Content: []byte("x")}, want: "unsupported mime type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Upload(ctx, principal, test.input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Upload = %v, want %q", err, test.want)
			}
		})
	}

	if _, err := service.Upload(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}, UploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", Content: []byte("x"),
	}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("unauthorized Upload = %v, want forbidden", err)
	}

	small := NewService(service.db, service.authz, service.objects, nil, 3, 30, Quota{})
	if _, err := small.Upload(ctx, principal, UploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", Content: []byte("too large"),
	}); err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("oversized Upload = %v", err)
	}
}

func TestUploadCreatesVersionPipelineAndOutbox(t *testing.T) {
	service, db, objects := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()

	result, err := service.Upload(ctx, principal, UploadInput{
		KnowledgeBaseID: "kb-a", Filename: "new.txt", MimeType: "text/plain", Content: []byte("new content"),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if result.Document.ID == "" || result.Version.Version != 1 || result.Pipeline.ID == "" {
		t.Fatalf("upload result = %+v", result)
	}
	if result.Version.Status != StatusStored || result.Document.Status != StatusStored {
		t.Fatalf("upload statuses = %s/%s", result.Document.Status, result.Version.Status)
	}
	if _, ok := objects.data[result.Version.SourceObjectKey]; !ok {
		t.Fatalf("source object %q was not stored", result.Version.SourceObjectKey)
	}
	var stages int64
	if err := db.Model(&models.StageRun{}).Where("pipeline_run_id = ?", result.Pipeline.ID).Count(&stages).Error; err != nil {
		t.Fatalf("count stages: %v", err)
	}
	if stages != 1 {
		t.Fatalf("stages = %d, want 1", stages)
	}
	var outbox models.OutboxEvent
	if err := db.First(&outbox, "aggregate_id = ?", result.Version.ID).Error; err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if outbox.EventType != "document.parse" {
		t.Fatalf("outbox event = %+v", outbox)
	}

	// The same content and knowledge base produce the same idempotency key, so
	// the retried upload reuses the original run instead of creating another.
	again, err := service.Upload(ctx, principal, UploadInput{
		KnowledgeBaseID: "kb-a", Filename: "new.txt", MimeType: "text/plain", Content: []byte("new content"),
	})
	if err != nil {
		t.Fatalf("duplicate Upload: %v", err)
	}
	if again.Pipeline.ID != result.Pipeline.ID || again.Version.ID != result.Version.ID {
		t.Fatalf("duplicate upload created a new run: %+v", again)
	}
}

func TestUploadSupersedesTheCurrentVersion(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	result, err := service.Upload(context.Background(), ownerPrincipal(), UploadInput{
		KnowledgeBaseID: "kb-a", DocumentID: "doc-a", Filename: "doc.txt", MimeType: "text/plain", Content: []byte("second version"),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if result.Version.Version != 2 || result.Document.LatestVersionID != result.Version.ID {
		t.Fatalf("superseding upload = %+v", result)
	}
	var previous models.DocumentVersion
	if err := db.First(&previous, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("load previous version: %v", err)
	}
	if previous.Status != StatusCancelled || previous.ErrorCode != "superseded" {
		t.Fatalf("previous version = %+v", previous)
	}
}

func TestUploadQuotaExceeded(t *testing.T) {
	service, _, _ := newReparseFixture(t)
	limited := NewService(service.db, service.authz, service.objects, nil, 1<<20, 30, Quota{MaxDocuments: 1})
	_, err := limited.Upload(context.Background(), ownerPrincipal(), UploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", Content: []byte("x"),
	})
	if !errors.Is(err, apperror.ErrQuotaExceeded) {
		t.Fatalf("Upload = %v, want quota exceeded", err)
	}
}

func TestIngestExternalValidationAndCreation(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()

	if _, err := service.IngestExternal(ctx, ExternalUploadInput{}); err == nil {
		t.Fatal("empty external upload must be rejected")
	}
	if _, err := service.IngestExternal(ctx, ExternalUploadInput{
		TenantID: "tenant-a", KnowledgeBaseID: "missing", DocumentID: "doc-ext", ActorID: "user-a",
		Filename: "ext.txt", Content: []byte("x"),
	}); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("unknown knowledge base = %v, want not found", err)
	}

	result, err := service.IngestExternal(ctx, ExternalUploadInput{
		TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-ext", ActorID: "user-a",
		Filename: "ext.txt", MimeType: "text/plain", Content: []byte("external content"),
	})
	if err != nil {
		t.Fatalf("IngestExternal: %v", err)
	}
	if result.Document.ID != "doc-ext" || result.Document.OwnerID != "user-a" || result.Version.Version != 1 {
		t.Fatalf("external result = %+v", result)
	}
	var stored models.Document
	if err := db.First(&stored, "id = ?", "doc-ext").Error; err != nil {
		t.Fatalf("load external document: %v", err)
	}
	if stored.DeletedAt != nil {
		t.Fatalf("external document must not be tombstoned: %+v", stored)
	}
}

func TestIngestExternalRestoresTombstone(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	markDocumentDeleted(t, db, "doc-a")
	result, err := service.IngestExternal(context.Background(), ExternalUploadInput{
		TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", ActorID: "user-a",
		Filename: "doc.txt", MimeType: "text/plain", Content: []byte("restored content"),
	})
	if err != nil {
		t.Fatalf("IngestExternal restore: %v", err)
	}
	if result.Document.ID != "doc-a" {
		t.Fatalf("restored document = %+v", result.Document)
	}
	var stored models.Document
	if err := db.First(&stored, "id = ?", "doc-a").Error; err != nil {
		t.Fatalf("load restored document: %v", err)
	}
	if stored.DeletedAt != nil {
		t.Fatalf("restored document still tombstoned: %+v", stored)
	}
}

func TestUploadDuplicateExistingVersion(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	now := time.Now()
	if err := db.Create(&models.PipelineRun{
		ID: "pipe-ver-1", PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-a", DocumentVersionID: "ver-1", RequestID: "req", TraceID: "trace",
		IdempotencyKey: "pipe-ver-1", Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	result, err := service.Upload(context.Background(), ownerPrincipal(), UploadInput{
		KnowledgeBaseID: "kb-a", DocumentID: "doc-a", Filename: "doc.txt", MimeType: "text/plain",
		Content: []byte("reparse idempotency fixture"),
	})
	if err != nil {
		t.Fatalf("duplicate upload: %v", err)
	}
	if result.Version.ID != "ver-1" || result.Pipeline.ID != "pipe-ver-1" {
		t.Fatalf("duplicate upload = %+v", result)
	}
}

func TestGetDocument(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.Create(&models.PipelineRun{
		ID: "pipe-ver-1", PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-a", DocumentVersionID: "ver-1", RequestID: "req", TraceID: "trace",
		IdempotencyKey: "pipe-ver-1", Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	view, err := service.Get(ctx, ownerPrincipal(), "doc-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if view.Document.ID != "doc-a" || view.Version.ID != "ver-1" || view.Pipeline.ID != "pipe-ver-1" {
		t.Fatalf("document view = %+v", view)
	}
	if _, err := service.Get(ctx, ownerPrincipal(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("Get missing = %v, want not found", err)
	}
	if _, err := service.Get(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}, "doc-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("Get unauthorized = %v, want forbidden", err)
	}

	if err := db.Create(&models.Document{
		ID: "doc-c", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "user-a",
		Filename: "c.txt", MimeType: "text/plain", Status: StatusStored, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document without version: %v", err)
	}
	bare, err := service.Get(ctx, ownerPrincipal(), "doc-c")
	if err != nil {
		t.Fatalf("Get document without version: %v", err)
	}
	if bare.Document.ID != "doc-c" || bare.Version.ID != "" {
		t.Fatalf("bare view = %+v", bare)
	}
}

func TestDeleteAndRestoreDocument(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()

	if err := service.Delete(ctx, principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("delete missing = %v, want not found", err)
	}
	if err := service.Delete(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}, "doc-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("delete unauthorized = %v, want forbidden", err)
	}
	if err := service.Delete(ctx, principal, "doc-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var document models.Document
	if err := db.First(&document, "id = ?", "doc-a").Error; err != nil {
		t.Fatalf("load deleted document: %v", err)
	}
	if document.DeletedAt == nil || document.PurgeAfter == nil || document.Status != StatusCancelled {
		t.Fatalf("deleted document = %+v", document)
	}
	if err := service.Delete(ctx, principal, "doc-a"); err != nil {
		t.Fatalf("second delete must be idempotent: %v", err)
	}

	if _, err := service.Restore(ctx, principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("restore missing = %v, want not found", err)
	}
	restored, err := service.Restore(ctx, principal, "doc-a")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.DeletedAt != nil || restored.RestoredAt == nil {
		t.Fatalf("restored document = %+v", restored)
	}
	if restored.Status != StatusEvidenceReady {
		t.Fatalf("restored status = %s, want evidence ready", restored.Status)
	}
	unchanged, err := service.Restore(ctx, principal, "doc-a")
	if err != nil {
		t.Fatalf("restore live document: %v", err)
	}
	if unchanged.ID != "doc-a" {
		t.Fatalf("restore live document = %+v", unchanged)
	}

	markDocumentDeleted(t, db, "doc-a")
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Update("purge_after", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("expire retention: %v", err)
	}
	if _, err := service.Restore(ctx, principal, "doc-a"); err == nil || !strings.Contains(err.Error(), "retention window") {
		t.Fatalf("restore expired = %v", err)
	}
}

func TestPurgeRemovesDerivedRows(t *testing.T) {
	service, db, objects := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()

	if err := service.Purge(ctx, principal, "doc-a"); err == nil || !strings.Contains(err.Error(), "must be deleted") {
		t.Fatalf("purge live document = %v", err)
	}
	markDocumentDeleted(t, db, "doc-a")
	if err := service.Purge(ctx, principal, "doc-a"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var documents, versions, pipelines int64
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Count(&documents).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if err := db.Model(&models.DocumentVersion{}).Where("document_id = ?", "doc-a").Count(&versions).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if err := db.Model(&models.PipelineRun{}).Where("document_id = ?", "doc-a").Count(&pipelines).Error; err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if documents != 0 || versions != 0 || pipelines != 0 {
		t.Fatalf("purge left rows: documents=%d versions=%d pipelines=%d", documents, versions, pipelines)
	}
	if _, ok := objects.data["source-key"]; ok {
		t.Fatal("purge must delete the source object")
	}
	var audit models.AuditLog
	if err := db.First(&audit, "action = ? AND resource_id = ?", "purge", "doc-a").Error; err != nil {
		t.Fatalf("load purge audit: %v", err)
	}
}

func TestPurgeExpiredBatches(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.Create(&models.Document{
		ID: "doc-b", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "user-a",
		Filename: "b.txt", MimeType: "text/plain", Status: StatusCancelled, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed second document: %v", err)
	}
	for _, id := range []string{"doc-a", "doc-b"} {
		markDocumentDeleted(t, db, id)
		if err := db.Model(&models.Document{}).Where("id = ?", id).Update("purge_after", now.Add(-time.Hour)).Error; err != nil {
			t.Fatalf("expire %s: %v", id, err)
		}
	}
	purged, err := service.PurgeExpired(ctx, 1)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if purged != 1 {
		t.Fatalf("first batch purged %d, want 1", purged)
	}
	purged, err = service.PurgeExpired(ctx, 1)
	if err != nil {
		t.Fatalf("second PurgeExpired: %v", err)
	}
	if purged != 1 {
		t.Fatalf("second batch purged %d, want 1", purged)
	}
	if purged, err = service.PurgeExpired(ctx, 0); err != nil || purged != 0 {
		t.Fatalf("empty PurgeExpired = %d, %v", purged, err)
	}
}

func TestTombstoneExternal(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	if err := service.TombstoneExternal(ctx, "tenant-a", "missing", "user-a"); err != nil {
		t.Fatalf("missing tombstone must be a no-op: %v", err)
	}
	if err := service.TombstoneExternal(ctx, "tenant-a", "doc-a", "user-a"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	var document models.Document
	if err := db.First(&document, "id = ?", "doc-a").Error; err != nil {
		t.Fatalf("load document: %v", err)
	}
	if document.DeletedAt == nil {
		t.Fatalf("document was not tombstoned: %+v", document)
	}
}
