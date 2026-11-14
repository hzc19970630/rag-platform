package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type recordingPublisher struct {
	published []Task
	removed   []Task
	err       error
}

func (p *recordingPublisher) Publish(_ context.Context, task Task) error {
	p.published = append(p.published, task)
	return p.err
}

func (p *recordingPublisher) RemoveArchived(_ context.Context, task Task) error {
	p.removed = append(p.removed, task)
	return p.err
}

func seedDeadLetterFixture(t *testing.T, db *gorm.DB) Task {
	t.Helper()
	now := time.Now()
	if err := db.Create(&models.StageRun{
		PipelineRunID: "run-1", Stage: StageParse, Status: StageFailed, Attempts: 3,
		IdempotencyKey: "run-1:parse", ErrorCode: "parser_error", ErrorMessage: "bad bytes",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: "run-1:parse", TaskType: TypeTask, Status: inboxProcessing,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	return Task{
		Envelope: Envelope{
			SchemaVersion: SchemaTask, TenantID: "tenant-a", KnowledgeBaseID: "kb-1",
			DocumentVersionID: "ver-1", StageRunID: "1", IdempotencyKey: "run-1:parse",
		},
		TaskType: TypeTask, Stage: StageParse,
	}
}

func TestDeadLetterWorkflow(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&models.AuditLog{}); err != nil {
		t.Fatalf("migrate audit logs: %v", err)
	}
	seedOperator(t, db, "tenant-a", "user-a", "owner")
	seedOperator(t, db, "tenant-a", "viewer-a", "viewer")
	publisher := &recordingPublisher{}
	inspector := NewTaskInspector(db, publisher)
	task := seedDeadLetterFixture(t, db)
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}

	if _, err := inspector.Inspect(ctx, principal, "  "); err == nil {
		t.Fatal("blank idempotency key must be rejected")
	}
	if err := inspector.RecordDeadLetter(ctx, task, errors.New("retry budget exhausted")); err != nil {
		t.Fatalf("RecordDeadLetter: %v", err)
	}
	var row models.TaskDeadLetter
	if err := db.First(&row, "idempotency_key = ?", "run-1:parse").Error; err != nil {
		t.Fatalf("load dead letter: %v", err)
	}
	if row.Status != deadLetterPending || row.Attempts != 3 || row.LastError != "retry budget exhausted" {
		t.Fatalf("dead letter = %+v", row)
	}
	if rows, err := inspector.ListDeadLetters(ctx, principal, "pending", 0); err != nil || len(rows) != 1 {
		t.Fatalf("ListDeadLetters = %d, %v", len(rows), err)
	}
	if rows, err := inspector.ListDeadLetters(ctx, principal, "", 1000); err != nil || len(rows) != 1 {
		t.Fatalf("ListDeadLetters unfiltered = %d, %v", len(rows), err)
	}
	if _, err := inspector.ListDeadLetters(ctx, authz.Principal{SubjectID: "viewer-a", TenantID: "tenant-a"}, "", 10); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer ListDeadLetters = %v, want forbidden", err)
	}

	if err := inspector.RetryDeadLetter(ctx, principal, row.ID); err != nil {
		t.Fatalf("RetryDeadLetter: %v", err)
	}
	if len(publisher.removed) != 1 || len(publisher.published) != 1 || publisher.published[0].Queue != "interactive" {
		t.Fatalf("publisher calls = removed=%v published=%+v", publisher.removed, publisher.published)
	}
	if err := db.First(&row, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload dead letter: %v", err)
	}
	if row.Status != deadLetterRetrying {
		t.Fatalf("dead letter status = %s, want retrying", row.Status)
	}
	if err := inspector.RetryDeadLetter(ctx, principal, row.ID); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("second retry = %v, want conflict", err)
	}
	if err := inspector.RetryDeadLetter(ctx, principal, 9999); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing retry = %v, want not found", err)
	}

	if err := inspector.DiscardDeadLetter(ctx, principal, row.ID); err != nil {
		t.Fatalf("DiscardDeadLetter: %v", err)
	}
	if err := db.First(&row, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload dead letter: %v", err)
	}
	if row.Status != deadLetterDiscarded || row.ResolvedAt == nil {
		t.Fatalf("discarded dead letter = %+v", row)
	}
	var audits int64
	if err := db.Model(&models.AuditLog{}).Where("tenant_id = ? AND resource_type = ?", "tenant-a", "dead_letter").Count(&audits).Error; err != nil {
		t.Fatalf("count operator audits: %v", err)
	}
	if audits != 2 {
		t.Fatalf("operator audits = %d, want 2", audits)
	}
}

func TestRetryDeadLetterRequiresOperator(t *testing.T) {
	db := newTestDB(t)
	seedOperator(t, db, "tenant-a", "user-a", "owner")
	inspector := NewTaskInspector(db, &recordingPublisher{})
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "api_key"}
	if err := inspector.RetryDeadLetter(context.Background(), principal, 1); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key retry = %v, want forbidden", err)
	}
}

func TestCancelPipeline(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&models.AuditLog{}); err != nil {
		t.Fatalf("migrate audit logs: %v", err)
	}
	seedOperator(t, db, "tenant-a", "user-a", "owner")
	seedOperator(t, db, "tenant-a", "viewer-a", "viewer")
	now := time.Now()
	if err := db.Create(&models.PipelineRun{
		ID: "run-1", PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-1",
		DocumentID: "doc-1", DocumentVersionID: "ver-1", IdempotencyKey: "run-1",
		Status: StatusParsing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&[]models.StageRun{
		{PipelineRunID: "run-1", Stage: StageParse, Status: StageProcessing, IdempotencyKey: "run-1:parse", CreatedAt: now, UpdatedAt: now},
		{PipelineRunID: "run-1", Stage: StageNormalize, Status: StagePending, IdempotencyKey: "run-1:normalize", CreatedAt: now, UpdatedAt: now},
		{PipelineRunID: "run-1", Stage: StageChunk, Status: StageSucceeded, IdempotencyKey: "run-1:chunk", CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed stages: %v", err)
	}
	inspector := NewTaskInspector(db, nil)
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
	if err := inspector.CancelPipeline(context.Background(), principal, "run-1"); err != nil {
		t.Fatalf("CancelPipeline: %v", err)
	}
	var pipeline models.PipelineRun
	if err := db.First(&pipeline, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load pipeline: %v", err)
	}
	if pipeline.Status != StatusCancelled || pipeline.ErrorCode != "cancelled" {
		t.Fatalf("pipeline = %+v", pipeline)
	}
	var cancelled, succeeded int64
	if err := db.Model(&models.StageRun{}).Where("pipeline_run_id = ? AND status = ?", "run-1", StageCancelled).Count(&cancelled).Error; err != nil {
		t.Fatalf("count cancelled stages: %v", err)
	}
	if err := db.Model(&models.StageRun{}).Where("pipeline_run_id = ? AND status = ?", "run-1", StageSucceeded).Count(&succeeded).Error; err != nil {
		t.Fatalf("count succeeded stages: %v", err)
	}
	if cancelled != 2 || succeeded != 1 {
		t.Fatalf("cancelled=%d succeeded=%d", cancelled, succeeded)
	}
	if err := inspector.CancelPipeline(context.Background(), principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing pipeline = %v, want not found", err)
	}
	if err := inspector.CancelPipeline(context.Background(), authz.Principal{SubjectID: "viewer-a", TenantID: "tenant-a"}, "run-1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer cancel = %v, want forbidden", err)
	}
	if err := inspector.DiscardDeadLetter(context.Background(), principal, 9999); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing discard = %v, want not found", err)
	}
}
