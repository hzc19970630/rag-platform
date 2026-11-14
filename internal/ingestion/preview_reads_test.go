package ingestion

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func newPreviewFixture(t *testing.T) (*Service, func() authz.Principal) {
	t.Helper()
	service, db, objects := newReparseFixture(t)
	owner := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
	now := time.Now().UTC()
	normalized := `{"schema_version":"NormalizedDocumentV1","title":"Doc","language":"en","blocks":[{"block_id":"b1","kind":"text","text":"hello","heading_path":"H","source_path":"doc.txt"}]}`
	objects.data["normalized-key"] = []byte(normalized)
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Updates(map[string]any{
		"normalized_key": "normalized-key", "status": StatusEvidenceReady, "version": 1,
	}).Error; err != nil {
		t.Fatalf("update version: %v", err)
	}
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Updates(map[string]any{
		"status": StatusEvidenceReady, "latest_version_id": "ver-1",
	}).Error; err != nil {
		t.Fatalf("update document: %v", err)
	}
	if err := db.Create(&models.Chunk{
		ID: "chunk-child", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-1",
		ParentID: "parent-1", Ordinal: 0, Content: "child text", ContentHash: "hash", Status: StatusEvidenceReady,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed child chunk: %v", err)
	}
	if err := db.Create(&models.PipelineRun{
		ID: "pipe-1", PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-a", DocumentVersionID: "ver-1", IdempotencyKey: "pipe-1", Status: StatusEvidenceReady,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: "pipe-1", Stage: StageParse, Status: StageSucceeded, IdempotencyKey: "pipe-1:parse",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	return service, func() authz.Principal { return owner }
}

func TestPreviewReadPaths(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()

	page, err := service.ListDocuments(ctx, principal(), "kb-a", "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Document.ID != "doc-a" {
		t.Fatalf("ListDocuments = %+v, %v", page, err)
	}
	if _, err := service.ListDocuments(ctx, principal(), "kb-a", "bad-cursor", 0); err == nil {
		t.Fatal("invalid cursor must be rejected")
	}

	view, err := service.Get(ctx, principal(), "doc-a")
	if err != nil || view.Version.ID != "ver-1" {
		t.Fatalf("Get = %+v, %v", view, err)
	}
	status, err := service.Status(ctx, principal(), "doc-a")
	if err != nil || !status.ContentReady || len(status.Stages) != 1 {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	preview, err := service.Preview(ctx, principal(), "doc-a")
	if err != nil || len(preview.Blocks) != 1 || preview.Blocks[0].Text != "hello" {
		t.Fatalf("Preview = %+v, %v", preview, err)
	}
	chunks, err := service.Chunks(ctx, principal(), "doc-a", 0, 0)
	if err != nil || len(chunks.Items) != 1 || chunks.Items[0].ChunkID != "chunk-child" {
		t.Fatalf("Chunks = %+v, %v", chunks, err)
	}
	chunk, err := service.ChunkDetail(ctx, principal(), "chunk-child")
	if err != nil || chunk.ChunkID != "chunk-child" {
		t.Fatalf("ChunkDetail = %+v, %v", chunk, err)
	}
	versions, err := service.ListVersions(ctx, principal(), "doc-a")
	if err != nil || len(versions.Items) != 1 {
		t.Fatalf("ListVersions = %+v, %v", versions, err)
	}
	source, err := service.SourceDocument(ctx, principal(), "doc-a")
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if source.Body != nil {
		content, readErr := io.ReadAll(source.Body)
		_ = source.Body.Close()
		if readErr != nil || len(content) == 0 {
			t.Fatalf("source body = %q, %v", content, readErr)
		}
	}
}

func TestPreviewReadGuards(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()
	if _, err := service.Get(ctx, principal(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing Get = %v", err)
	}
	if _, err := service.ChunkDetail(ctx, principal(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing chunk = %v", err)
	}
	if err := service.db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Update("status", StatusChunked).Error; err != nil {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := service.Preview(ctx, principal(), "doc-a"); !errors.Is(err, apperror.ErrNotReady) {
		t.Fatalf("unready Preview = %v", err)
	}
	if _, err := service.Chunks(ctx, principal(), "doc-a", 0, 0); !errors.Is(err, apperror.ErrNotReady) {
		t.Fatalf("unready Chunks = %v", err)
	}
	if _, err := service.SourceDocument(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}, "doc-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("unauthorized source = %v", err)
	}
}
