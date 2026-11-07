package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newRetentionFixture(t *testing.T) (*Service, *gorm.DB) {
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
	if err := db.AutoMigrate(&models.PipelineRun{}, &models.StageRun{}, &models.OutboxEvent{},
		&models.TaskInbox{}, &models.TaskDeadLetter{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return &Service{db: db}, db
}

func seedPipeline(t *testing.T, db *gorm.DB, id, versionID, status string, updatedAt time.Time) {
	t.Helper()
	run := models.PipelineRun{
		ID: id, PipelineType: PipelineTypeEvidence, TenantID: "t1", KnowledgeBaseID: "kb-1",
		DocumentID: "doc-1", DocumentVersionID: versionID, RequestID: "req-" + id, TraceID: "trace-" + id,
		IdempotencyKey: "upload-" + id, Status: status, CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create pipeline %s: %v", id, err)
	}
	stageKey := id + ":" + StageParse
	stage := models.StageRun{
		PipelineRunID: id, Stage: StageParse, Status: StageSucceeded,
		IdempotencyKey: stageKey, CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := db.Create(&stage).Error; err != nil {
		t.Fatalf("create stage %s: %v", id, err)
	}
	outbox := []models.OutboxEvent{
		{TenantID: "t1", AggregateType: "pipeline_run", AggregateID: id, EventType: "document.parse",
			IdempotencyKey: stageKey, Payload: "{}", CreatedAt: updatedAt},
		{TenantID: "t1", AggregateType: "document_version", AggregateID: versionID, EventType: "document.parse",
			IdempotencyKey: "outbox-" + id, Payload: "{}", CreatedAt: updatedAt},
	}
	if err := db.Create(&outbox).Error; err != nil {
		t.Fatalf("create outbox for %s: %v", id, err)
	}
	inbox := models.TaskInbox{
		IdempotencyKey: stageKey, TaskType: TypeTask, Status: inboxSucceeded,
		CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	if err := db.Create(&inbox).Error; err != nil {
		t.Fatalf("create inbox for %s: %v", id, err)
	}
}

func seedRetentionDeadLetter(t *testing.T, db *gorm.DB, key, status string, resolvedAt *time.Time) {
	t.Helper()
	createdAt := time.Now().Add(-120 * 24 * time.Hour)
	letter := models.TaskDeadLetter{
		IdempotencyKey: key, TenantID: "t1", KnowledgeBaseID: "kb-1", DocumentVersionID: "ver-" + key,
		TaskType: TypeTask, Stage: StageParse, Attempts: 5, LastError: "boom", Payload: "{}",
		Status: status, ResolvedAt: resolvedAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := db.Create(&letter).Error; err != nil {
		t.Fatalf("create dead letter %s: %v", key, err)
	}
}

func TestPurgeTaskHistoryKeepsRecentAndInFlightRows(t *testing.T) {
	service, db := newRetentionFixture(t)
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	recent := cutoff.Add(24 * time.Hour)

	seedPipeline(t, db, "pipe-old", "ver-old", StatusEvidenceReady, old)
	seedPipeline(t, db, "pipe-recent", "ver-recent", StatusEvidenceReady, recent)
	seedPipeline(t, db, "pipe-active", "ver-active", StatusParsing, old)
	resolvedOld := old.Add(time.Hour)
	seedRetentionDeadLetter(t, db, "dl-resolved-old", pipeline.DeadLetterResolved, &resolvedOld)
	seedRetentionDeadLetter(t, db, "dl-discarded-old", pipeline.DeadLetterDiscarded, &resolvedOld)
	resolvedRecent := cutoff.Add(time.Hour)
	seedRetentionDeadLetter(t, db, "dl-resolved-recent", pipeline.DeadLetterResolved, &resolvedRecent)
	seedRetentionDeadLetter(t, db, "dl-pending-old", pipeline.DeadLetterPending, nil)

	purged, err := service.PurgeTaskHistory(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("purge task history: %v", err)
	}
	if purged != 3 {
		t.Fatalf("purged %d rows, want 1 pipeline + 2 dead letters", purged)
	}

	var pipelineCount int64
	db.Model(&models.PipelineRun{}).Where("id = ?", "pipe-old").Count(&pipelineCount)
	if pipelineCount != 0 {
		t.Fatal("expired terminal pipeline was not purged")
	}
	for _, table := range []any{&models.StageRun{}, &models.OutboxEvent{}, &models.TaskInbox{}} {
		var count int64
		if err := db.Model(table).Where("idempotency_key LIKE ?", "pipe-old:%").Count(&count).Error; err != nil {
			t.Fatalf("count %T: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%T kept rows for the purged pipeline", table)
		}
	}
	var versionOutbox int64
	db.Model(&models.OutboxEvent{}).Where("aggregate_id = ?", "ver-old").Count(&versionOutbox)
	if versionOutbox != 0 {
		t.Fatal("document_version outbox event survived the purge")
	}

	for _, id := range []string{"pipe-recent", "pipe-active"} {
		var count int64
		db.Model(&models.PipelineRun{}).Where("id = ?", id).Count(&count)
		if count != 1 {
			t.Fatalf("%s must be kept", id)
		}
	}
	var inboxCount int64
	db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", "pipe-recent:"+StageParse).Count(&inboxCount)
	if inboxCount != 1 {
		t.Fatal("recent pipeline lost its inbox row")
	}
	var deadLetters int64
	db.Model(&models.TaskDeadLetter{}).Where("idempotency_key LIKE ?", "dl-resolved-old").Count(&deadLetters)
	if deadLetters != 0 {
		t.Fatal("resolved expired dead letter was not purged")
	}
	for _, key := range []string{"dl-resolved-recent", "dl-pending-old"} {
		db.Model(&models.TaskDeadLetter{}).Where("idempotency_key = ?", key).Count(&deadLetters)
		if deadLetters != 1 {
			t.Fatalf("%s must be kept", key)
		}
	}
}

func TestPurgeTaskHistoryHonorsBatchLimit(t *testing.T) {
	service, db := newRetentionFixture(t)
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	old := cutoff.Add(-time.Hour)
	seedPipeline(t, db, "pipe-a", "ver-a", StatusFailed, old)
	seedPipeline(t, db, "pipe-b", "ver-b", StatusCancelled, old)

	purged, err := service.PurgeTaskHistory(context.Background(), cutoff, 1)
	if err != nil {
		t.Fatalf("purge first batch: %v", err)
	}
	if purged != 1 {
		t.Fatalf("first batch purged %d rows, want 1", purged)
	}
	var remaining int64
	db.Model(&models.PipelineRun{}).Count(&remaining)
	if remaining != 1 {
		t.Fatalf("%d pipelines remain, want 1", remaining)
	}
	purged, err = service.PurgeTaskHistory(context.Background(), cutoff, 1)
	if err != nil || purged != 1 {
		t.Fatalf("second batch purged %d rows (err %v), want 1", purged, err)
	}
	db.Model(&models.PipelineRun{}).Count(&remaining)
	if remaining != 0 {
		t.Fatalf("%d pipelines remain after the second batch", remaining)
	}
}
