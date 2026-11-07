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
