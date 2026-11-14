package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeStore struct {
	snapshot         Snapshot
	platform         PlatformSnapshot
	err              error
	lastTenant       string
	lastSince        time.Time
	lastAttempts     int
	lastPlatformTime time.Time
}

func (f *fakeStore) Snapshot(_ context.Context, tenantID string, since time.Time, attempts int) (Snapshot, error) {
	f.lastTenant = tenantID
	f.lastSince = since
	f.lastAttempts = attempts
	return f.snapshot, f.err
}

func (f *fakeStore) PlatformSnapshot(_ context.Context, now time.Time, attempts int) (PlatformSnapshot, error) {
	f.lastPlatformTime = now
	f.lastAttempts = attempts
	return f.platform, f.err
}

func TestServiceDelegatesToStore(t *testing.T) {
	store := &fakeStore{
		snapshot: Snapshot{OutboxBacklog: 3, ParseFailureRate: 0.5},
		platform: PlatformSnapshot{OutboxBacklog: 7},
	}
	service := NewService(store)
	since := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	snapshot, err := service.Snapshot(context.Background(), "tenant-a", since, 5)
	if err != nil || snapshot.OutboxBacklog != 3 || store.lastTenant != "tenant-a" || store.lastAttempts != 5 || !store.lastSince.Equal(since) {
		t.Fatalf("Snapshot = %+v, %v, store=%+v", snapshot, err, store)
	}
	platform, err := service.PlatformSnapshot(context.Background(), since, 9)
	if err != nil || platform.OutboxBacklog != 7 || store.lastAttempts != 9 || !store.lastPlatformTime.Equal(since) {
		t.Fatalf("PlatformSnapshot = %+v, %v, store=%+v", platform, err, store)
	}
	store.err = errors.New("store down")
	if _, err := service.Snapshot(context.Background(), "tenant-a", since, 5); err == nil {
		t.Fatal("Snapshot must propagate the store error")
	}
	if _, err := service.PlatformSnapshot(context.Background(), since, 5); err == nil {
		t.Fatal("PlatformSnapshot must propagate the store error")
	}
}

func TestPostgresSnapshotCountsTenantSignals(t *testing.T) {
	db := openOpsDB(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	seed := []any{
		&models.PipelineRun{ID: "run-a", PipelineType: "evidence", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-a", IdempotencyKey: "run-a", Status: "PARSED", CreatedAt: now, UpdatedAt: now},
		&models.PipelineRun{ID: "run-b", PipelineType: "evidence", TenantID: "tenant-b", KnowledgeBaseID: "kb-b", DocumentID: "doc-b", DocumentVersionID: "ver-b", IdempotencyKey: "run-b", Status: "PARSED", CreatedAt: now, UpdatedAt: now},
		&models.StageRun{PipelineRunID: "run-a", Stage: "parse", Status: "SUCCEEDED", IdempotencyKey: "run-a:parse", CreatedAt: now, UpdatedAt: now},
		&models.StageRun{PipelineRunID: "run-a", Stage: "parse", Status: "FAILED", IdempotencyKey: "run-a:parse-2", CreatedAt: now, UpdatedAt: now},
		&models.StageRun{PipelineRunID: "run-b", Stage: "parse", Status: "FAILED", IdempotencyKey: "run-b:parse", CreatedAt: now, UpdatedAt: now},
		&models.Message{ID: "msg-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ConversationID: "conv-a", UserID: "user-a", RequestID: "req-a", Role: "assistant", Status: "FAILED", Content: "x", CreatedAt: now, UpdatedAt: now},
		&models.TaskDeadLetter{IdempotencyKey: "dl-a", TenantID: "tenant-a", Status: "PENDING", TaskType: "t", Stage: "parse", Payload: "{}", CreatedAt: now, UpdatedAt: now},
		&models.TaskDeadLetter{IdempotencyKey: "dl-b", TenantID: "tenant-a", Status: "RETRYING", TaskType: "t", Stage: "parse", Payload: "{}", CreatedAt: now, UpdatedAt: now},
		&models.TaskDeadLetter{IdempotencyKey: "dl-c", TenantID: "tenant-b", Status: "PENDING", TaskType: "t", Stage: "parse", Payload: "{}", CreatedAt: now, UpdatedAt: now},
		&models.OutboxEvent{TenantID: "tenant-a", AggregateType: "a", AggregateID: "1", EventType: "e", IdempotencyKey: "o1", Payload: "{}", Attempts: 1},
		&models.OutboxEvent{TenantID: "tenant-a", AggregateType: "a", AggregateID: "2", EventType: "e", IdempotencyKey: "o2", Payload: "{}", Attempts: 1, PublishedAt: &now},
	}
	for _, row := range seed {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	snapshot, err := NewPostgresStore(db).Snapshot(context.Background(), "tenant-a", since, 10)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.ParseFailureRate != 0.5 || snapshot.ModelErrors24h != 1 || snapshot.OutboxBacklog != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Stages["parse"]["succeeded"] != 1 || snapshot.Stages["parse"]["failed"] != 1 {
		t.Fatalf("stage counts = %+v", snapshot.Stages)
	}
	if snapshot.DeadLetters["PENDING"] != 1 || snapshot.DeadLetters["RETRYING"] != 1 {
		t.Fatalf("dead letters = %+v", snapshot.DeadLetters)
	}
}

func TestPostgresSnapshotErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	store := NewPostgresStore(db)
	if _, err := store.Snapshot(context.Background(), "tenant-a", time.Now(), 10); err == nil {
		t.Fatal("Snapshot must fail without its tables")
	}
	if _, err := store.PlatformSnapshot(context.Background(), time.Now(), 10); err == nil {
		t.Fatal("PlatformSnapshot must fail without its tables")
	}
}

func openOpsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.PipelineRun{}, &models.StageRun{}, &models.Message{}, &models.TaskDeadLetter{}, &models.OutboxEvent{},
	); err != nil {
		t.Fatalf("migrate ops tables: %v", err)
	}
	return db
}
