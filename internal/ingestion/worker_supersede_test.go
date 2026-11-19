package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type supersededIndexWriter struct {
	*recordingIndexWriter
}

func (w supersededIndexWriter) WriteEmbeddings(context.Context, string, string, []EmbeddingRecord) error {
	return apperror.ErrVersionSuperseded
}

func setPipelineStatus(t *testing.T, worker *PipelineWorker, status string) {
	t.Helper()
	if err := worker.db.Model(&models.PipelineRun{}).Where("id = ?", "run-1").Update("status", status).Error; err != nil {
		t.Fatalf("set pipeline status: %v", err)
	}
}

func setVersionCancelled(t *testing.T, worker *PipelineWorker) {
	t.Helper()
	if err := worker.db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Update("status", StatusCancelled).Error; err != nil {
		t.Fatalf("cancel version: %v", err)
	}
}

func assertSupersededOutcome(t *testing.T, worker *PipelineWorker, task Task) {
	t.Helper()
	var version models.DocumentVersion
	if err := worker.db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if version.Status != StatusCancelled {
		t.Fatalf("superseded version was written back to %q", version.Status)
	}
	var stage models.StageRun
	if err := worker.db.First(&stage, "idempotency_key = ?", task.IdempotencyKey).Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stage.Status != StageCancelled {
		t.Fatalf("stage status = %q, want CANCELLED", stage.Status)
	}
	if inbox := loadInbox(t, worker, task.IdempotencyKey); inbox.Status != inboxSucceeded {
		t.Fatalf("inbox status = %q, want SUCCEEDED", inbox.Status)
	}
}

func TestParseAcknowledgesSupersededVersion(t *testing.T) {
	worker, _, _, _, _ := newWorkerFixture(t)
	setVersionCancelled(t, worker)
	task := workerTaskForStage(t, worker.db, StageParse)
	if err := worker.Process(context.Background(), task); err != nil {
		t.Fatalf("parse superseded version: %v", err)
	}
	assertSupersededOutcome(t, worker, task)
}

func TestChunkAcknowledgesSupersededVersion(t *testing.T) {
	worker, db, _, _, _ := newWorkerFixture(t)
	setVersionCancelled(t, worker)
	setPipelineStatus(t, worker, StatusNormalized)
	task := workerTaskForStage(t, db, StageChunk)
	if err := worker.Process(context.Background(), task); err != nil {
		t.Fatalf("chunk superseded version: %v", err)
	}
	assertSupersededOutcome(t, worker, task)
	var chunks int64
	if err := db.Model(&models.Chunk{}).Where("document_version_id = ?", "ver-1").Count(&chunks).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunks != 0 {
		t.Fatalf("superseded version kept %d chunks", chunks)
	}
}

func TestIndexAcknowledgesSupersededVersion(t *testing.T) {
	worker, db, _, _, _ := newWorkerFixture(t)
	setVersionCancelled(t, worker)
	setPipelineStatus(t, worker, StatusEmbedded)
	task := workerTaskForStage(t, db, StageIndex)
	if err := worker.Process(context.Background(), task); err != nil {
		t.Fatalf("index superseded version: %v", err)
	}
	assertSupersededOutcome(t, worker, task)
	var packages int64
	if err := db.Model(&models.EvidencePackage{}).Where("document_version_id = ?", "ver-1").Count(&packages).Error; err != nil {
		t.Fatalf("count packages: %v", err)
	}
	if packages != 0 {
		t.Fatalf("superseded version published %d evidence packages", packages)
	}
}

// The adapter fences its own transaction, but the worker must also map that
// signal to a cancelled stage instead of a retryable failure.
func TestEmbedAcknowledgesAdapterSupersedeSignal(t *testing.T) {
	worker, db, objects, writer, reader := newWorkerFixture(t)
	now := time.Now()
	for _, chunk := range []models.Chunk{
		{ID: "parent-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-1", Ordinal: 0, Content: "hello", ContentHash: "h1", Status: StatusParent, CreatedAt: now, UpdatedAt: now},
		{ID: "child-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-1", ParentID: "parent-1", Ordinal: 0, Content: "hello", ContentHash: "h2", Status: StatusChunked, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&chunk).Error; err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Update("status", StatusChunked).Error; err != nil {
		t.Fatalf("set version chunked: %v", err)
	}
	setPipelineStatus(t, worker, StatusChunked)
	superseded := NewPipelineWorker(db, objects, reader, rebuildEmbedder{dimension: 8, model: "test-embed"}, supersededIndexWriter{writer}, nil, 0)
	task := workerTaskForStage(t, db, StageEmbed)
	if err := superseded.Process(context.Background(), task); err != nil {
		t.Fatalf("embed superseded version: %v", err)
	}
	var stage models.StageRun
	if err := db.First(&stage, "idempotency_key = ?", task.IdempotencyKey).Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stage.Status != StageCancelled {
		t.Fatalf("stage status = %q, want CANCELLED", stage.Status)
	}
	var version models.DocumentVersion
	if err := db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if version.Status != StatusChunked {
		t.Fatalf("version status = %q, want CHUNKED", version.Status)
	}
}
