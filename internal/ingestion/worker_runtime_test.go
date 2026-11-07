package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func seedWorkerStage(t *testing.T, db *gorm.DB, pipelineID, stage, status string) Task {
	t.Helper()
	now := time.Now()
	key := pipelineID + ":" + stage
	if err := db.Create(&models.PipelineRun{
		ID: pipelineID, PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-1",
		DocumentID: "doc-1", DocumentVersionID: "ver-1", RequestID: "req-1", TraceID: "trace-1",
		IdempotencyKey: "doc-1:ver-1:" + stage, Status: status, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	stageRow := models.StageRun{PipelineRunID: pipelineID, Stage: stage, Status: StageProcessing, IdempotencyKey: key, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&stageRow).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: key, TaskType: TypeTask, Status: inboxProcessing, StageRunID: stageRow.ID,
		LeaseOwner: "worker-a", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	return Task{
		Envelope: Envelope{
			TenantID: "tenant-a", KnowledgeBaseID: "kb-1", DocumentID: "doc-1", DocumentVersionID: "ver-1",
			PipelineRunID: pipelineID, StageRunID: "1", IdempotencyKey: key,
		},
		TaskType: TypeTask, Stage: stage,
	}
}

func TestCompleteStageChainsNextStageThroughPipelineRuntime(t *testing.T) {
	db := newTestDB(t)
	task := seedWorkerStage(t, db, "run-1", StageParse, StatusParsing)
	if err := (&PipelineWorker{db: db}).completeStage(context.Background(), task); err != nil {
		t.Fatalf("complete stage: %v", err)
	}
	var pipelineRun models.PipelineRun
	if err := db.First(&pipelineRun, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load pipeline: %v", err)
	}
	if pipelineRun.Status != StatusParsed {
		t.Fatalf("pipeline status = %q, want %q", pipelineRun.Status, StatusParsed)
	}
	var stage models.StageRun
	if err := db.First(&stage, "idempotency_key = ?", "run-1:parse").Error; err != nil {
		t.Fatalf("load parse stage: %v", err)
	}
	if stage.Status != StageSucceeded || stage.CompletedAt == nil {
		t.Fatalf("parse stage = %+v", stage)
	}
	if inbox := loadInbox(t, &PipelineWorker{db: db}, "run-1:parse"); inbox.Status != inboxSucceeded || inbox.LeaseOwner != "" {
		t.Fatalf("parse inbox = %+v", inbox)
	}
	var next models.StageRun
	if err := db.First(&next, "idempotency_key = ?", "run-1:normalize").Error; err != nil {
		t.Fatalf("load next stage: %v", err)
	}
	if next.Stage != StageNormalize || next.Status != StagePending {
		t.Fatalf("next stage = %+v", next)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", "run-1:normalize").Error; err != nil {
		t.Fatalf("load outbox event: %v", err)
	}
	if event.EventType != "document.normalize" || event.AggregateType != "pipeline_run" || event.AggregateID != "run-1" {
		t.Fatalf("outbox event = %+v", event)
	}
	var payload Task
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Stage != StageNormalize || payload.PipelineRunID != "run-1" || payload.IdempotencyKey != "run-1:normalize" {
		t.Fatalf("next task payload = %+v", payload)
	}
}

func TestProcessPropagatesCurrencyCheckFailure(t *testing.T) {
	db := newTestDB(t)
	task := seedWorkerStage(t, db, "run-1", StageParse, StatusParsing)
	task.SchemaVersion = SchemaTask
	if err := db.Callback().Query().Before("gorm:query").Register("test:fail_currency_lookup", func(tx *gorm.DB) {
		if tx.Statement.TableExpr != nil && strings.Contains(tx.Statement.TableExpr.SQL, "documents d") {
			tx.AddError(errors.New("injected currency lookup failure"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	err := (&PipelineWorker{db: db}).Process(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "injected currency lookup failure") {
		t.Fatalf("expected the currency lookup failure to propagate, got %v", err)
	}
	// A transient lookup error must retry. Settling the inbox would silently
	// drop a document version that is still current.
	if apperror.IsTerminal(err) {
		t.Fatalf("currency lookup failure must stay retryable, got %v", err)
	}
	inbox := loadInbox(t, &PipelineWorker{db: db}, "run-1:parse")
	if inbox.Status != inboxProcessing || inbox.LeaseOwner != "" || inbox.LeaseUntil != nil {
		t.Fatalf("inbox must stay claimable for the retry, got %+v", inbox)
	}
	var stage models.StageRun
	if err := db.First(&stage, "idempotency_key = ?", "run-1:parse").Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stage.Status != StageProcessing || stage.CompletedAt != nil {
		t.Fatalf("stage must not advance on a lookup failure, got %+v", stage)
	}
	var pipelineRun models.PipelineRun
	if err := db.First(&pipelineRun, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load pipeline: %v", err)
	}
	if pipelineRun.Status != StatusParsing {
		t.Fatalf("pipeline status = %q, want %q", pipelineRun.Status, StatusParsing)
	}
}

func TestProcessSettlesStaleVersionWithoutRunningStage(t *testing.T) {
	db := newTestDB(t)
	task := seedWorkerStage(t, db, "run-1", StageParse, StatusParsing)
	task.SchemaVersion = SchemaTask
	if err := (&PipelineWorker{db: db}).Process(context.Background(), task); err != nil {
		t.Fatalf("a stale version must settle quietly, got %v", err)
	}
	inbox := loadInbox(t, &PipelineWorker{db: db}, "run-1:parse")
	if inbox.Status != inboxSucceeded || inbox.LeaseUntil != nil {
		t.Fatalf("stale inbox = %+v", inbox)
	}
	var stage models.StageRun
	if err := db.First(&stage, "idempotency_key = ?", "run-1:parse").Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stage.Status != StageProcessing {
		t.Fatalf("a stale version must not run the stage, got %+v", stage)
	}
}

func TestCompleteFinalStageEndsChain(t *testing.T) {
	db := newTestDB(t)
	task := seedWorkerStage(t, db, "run-1", StageIndex, StatusIndexing)
	if err := (&PipelineWorker{db: db}).completeStage(context.Background(), task); err != nil {
		t.Fatalf("complete stage: %v", err)
	}
	var pipelineRun models.PipelineRun
	if err := db.First(&pipelineRun, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load pipeline: %v", err)
	}
	if pipelineRun.Status != StatusEvidenceReady {
		t.Fatalf("pipeline status = %q, want %q", pipelineRun.Status, StatusEvidenceReady)
	}
	var count int64
	if err := db.Model(&models.OutboxEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if count != 0 {
		t.Fatalf("final stage must not enqueue another event, found %d", count)
	}
}

func TestFailStageMarksPipelineFailed(t *testing.T) {
	db := newTestDB(t)
	task := seedWorkerStage(t, db, "run-1", StageParse, StatusParsing)
	now := time.Now()
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-1", DocumentID: "doc-1",
		Status: StatusParsing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := (&PipelineWorker{db: db}).failStage(context.Background(), task, "parser_error", "bad bytes"); err != nil {
		t.Fatalf("fail stage: %v", err)
	}
	var stage models.StageRun
	if err := db.First(&stage, "idempotency_key = ?", "run-1:parse").Error; err != nil {
		t.Fatalf("load stage: %v", err)
	}
	if stage.Status != StageFailed || stage.ErrorCode != "parser_error" || stage.ErrorMessage != "bad bytes" {
		t.Fatalf("failed stage = %+v", stage)
	}
	var pipelineRun models.PipelineRun
	if err := db.First(&pipelineRun, "id = ?", "run-1").Error; err != nil {
		t.Fatalf("load pipeline: %v", err)
	}
	if pipelineRun.Status != StatusFailed || pipelineRun.ErrorCode != "parser_error" {
		t.Fatalf("failed pipeline = %+v", pipelineRun)
	}
	var version models.DocumentVersion
	if err := db.First(&version, "id = ?", "ver-1").Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if version.Status != StatusFailed {
		t.Fatalf("version status = %q", version.Status)
	}
}
