package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// A single connection keeps every statement on the same in-memory database.
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.Document{},
		&models.DocumentVersion{},
		&models.EvidencePackage{},
		&models.PipelineRun{},
		&models.StageRun{},
		&models.TaskInbox{},
		&models.TaskDeadLetter{},
		&models.OutboxEvent{},
		&models.ProjectionRun{},
		&models.ProjectionStageRun{},
		&models.SyncRun{},
		&models.AnswerJob{},
		&models.Membership{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedOperator(t *testing.T, db *gorm.DB, tenantID, userID, role string) {
	t.Helper()
	now := time.Now()
	if err := db.Create(&models.Membership{
		TenantID: tenantID, UserID: userID, Role: role, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed operator: %v", err)
	}
}

// A tenant operator must not be able to read another tenant's inbox row just by
// guessing its idempotency key. The inbox table has no tenant column, so the
// lookup has to be corroborated by a tenant-owned pipeline, outbox event or
// dead letter.
func TestInspectDoesNotLeakOtherTenantInbox(t *testing.T) {
	db := newTestDB(t)
	seedOperator(t, db, "tenant-a", "user-a", "admin")
	now := time.Now()
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: "tenant-b:parse",
		TaskType:       TypeTask,
		Status:         inboxProcessing,
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}

	inspector := NewTaskInspector(db, nil)
	inspection, err := inspector.Inspect(context.Background(), authz.Principal{
		SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session",
	}, "tenant-b:parse")

	if !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected not_found for another tenant's inbox, got err=%v inspection=%+v", err, inspection)
	}
	if inspection.Inbox != nil {
		t.Fatalf("leaked another tenant's inbox: %+v", inspection.Inbox)
	}
}

func TestInspectReturnsOwnTenantTask(t *testing.T) {
	db := newTestDB(t)
	seedOperator(t, db, "tenant-a", "user-a", "admin")
	now := time.Now()
	const key = "tenant-a:parse"
	if err := db.Create(&models.PipelineRun{
		ID: "pipe-a", PipelineType: PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-a", DocumentVersionID: "ver-a", RequestID: "req", TraceID: "trace",
		IdempotencyKey: "pipe-key-a", Status: StatusParsing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: "pipe-a", Stage: StageParse, Status: StageProcessing,
		IdempotencyKey: key, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: key, TaskType: TypeTask, Status: inboxProcessing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}

	inspector := NewTaskInspector(db, nil)
	inspection, err := inspector.Inspect(context.Background(), authz.Principal{
		SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session",
	}, key)
	if err != nil {
		t.Fatalf("inspect own task: %v", err)
	}
	if inspection.Inbox == nil || inspection.Pipeline == nil || inspection.Stage == nil {
		t.Fatalf("incomplete inspection: %+v", inspection)
	}
}

func TestInspectResolvesProjectionSyncAndAnswerKeys(t *testing.T) {
	db := newTestDB(t)
	seedOperator(t, db, "tenant-a", "user-a", "admin")
	now := time.Now()
	rows := []any{
		&models.ProjectionRun{
			ID: "prj-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", EvidencePackageID: "pkg-a",
			ProjectionType: "wiki", Status: status.WikiReady, CreatedAt: now, UpdatedAt: now,
		},
		&models.ProjectionStageRun{
			ProjectionRunID: "prj-a", ProjectionType: "wiki", Stage: "GENERATE",
			Status: status.Succeeded, Attempts: 2, IdempotencyKey: "prj-a:GENERATE", CreatedAt: now, UpdatedAt: now,
		},
		&models.OutboxEvent{
			TenantID: "tenant-a", AggregateType: "projection_run", AggregateID: "prj-a",
			EventType: "projection.wiki", IdempotencyKey: "prj-a:GENERATE", Payload: "{}", PublishedAt: &now,
		},
		&models.SyncRun{
			ID: "sync-a", TenantID: "tenant-a", ConnectorID: "conn-a", KnowledgeBaseID: "kb-a",
			Mode: "full", Status: status.SyncPartial, Attempts: 1, ErrorSummary: "one item failed", CreatedAt: now, UpdatedAt: now,
		},
		&models.AnswerJob{
			ID: "job-a", TenantID: "tenant-a", UserID: "user-a", ConversationID: "conv-a",
			RequestID: "req-a", Status: status.AnswerCompleted, Attempts: 1, CreatedAt: now, UpdatedAt: now,
		},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	inspector := NewTaskInspector(db, nil)
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}

	projection, err := inspector.Inspect(context.Background(), principal, "prj-a:GENERATE")
	if err != nil {
		t.Fatalf("inspect projection: %v", err)
	}
	if projection.Kind != "projection" || len(projection.Mechanisms) < 2 {
		t.Fatalf("projection inspection = %+v", projection)
	}

	syncInspection, err := inspector.Inspect(context.Background(), principal, "connector-sync:sync-a")
	if err != nil {
		t.Fatalf("inspect sync: %v", err)
	}
	if syncInspection.Kind != "sync" || syncInspection.Mechanisms[0].Mechanism != status.MechanismSyncRetry {
		t.Fatalf("sync inspection = %+v", syncInspection)
	}

	answerInspection, err := inspector.Inspect(context.Background(), principal, "job-a")
	if err != nil {
		t.Fatalf("inspect answer: %v", err)
	}
	if answerInspection.Kind != "answer" || answerInspection.Mechanisms[0].Mechanism != status.MechanismAnswerLease {
		t.Fatalf("answer inspection = %+v", answerInspection)
	}
}

func TestInspectDoesNotLeakOtherTenantProjection(t *testing.T) {
	db := newTestDB(t)
	seedOperator(t, db, "tenant-a", "user-a", "admin")
	now := time.Now()
	if err := db.Create(&models.ProjectionRun{
		ID: "prj-b", TenantID: "tenant-b", KnowledgeBaseID: "kb-b", EvidencePackageID: "pkg-b",
		ProjectionType: "wiki", Status: status.WikiReady, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed projection run: %v", err)
	}
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: "prj-b", ProjectionType: "wiki", Stage: "COLLECT",
		Status: status.Succeeded, IdempotencyKey: "prj-b:COLLECT", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed projection stage: %v", err)
	}
	_, err := NewTaskInspector(db, nil).Inspect(context.Background(), authz.Principal{
		SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session",
	}, "prj-b:COLLECT")
	if !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("cross-tenant projection lookup err = %v, want not_found", err)
	}
}
