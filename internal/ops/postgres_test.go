package ops

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPlatformSnapshotCountsBacklogAndLag(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.OutboxEvent{}, &models.TaskInbox{}, &models.ProjectionRun{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	old := now.Add(-10 * time.Minute)
	rows := []any{
		&models.OutboxEvent{TenantID: "t", AggregateType: "a", AggregateID: "1", EventType: "e", IdempotencyKey: "o1", Payload: "{}", Attempts: 1},
		&models.OutboxEvent{TenantID: "t", AggregateType: "a", AggregateID: "2", EventType: "e", IdempotencyKey: "o2", Payload: "{}", Attempts: 1, PublishedAt: &now},
		&models.TaskInbox{IdempotencyKey: "i1", TaskType: "ingestion", Status: "PROCESSING", LeaseUntil: &past},
		&models.TaskInbox{IdempotencyKey: "i2", TaskType: "ingestion", Status: "PROCESSING", LeaseUntil: &future},
		&models.TaskInbox{IdempotencyKey: "i3", TaskType: "ingestion", Status: "SUCCEEDED", LeaseUntil: &past},
		&models.ProjectionRun{ID: "p1", TenantID: "t", KnowledgeBaseID: "kb", EvidencePackageID: "pkg", ProjectionType: "wiki", Status: "WIKI_PENDING", CreatedAt: old},
		&models.ProjectionRun{ID: "p2", TenantID: "t", KnowledgeBaseID: "kb", EvidencePackageID: "pkg", ProjectionType: "wiki", Status: "WIKI_READY", CreatedAt: old},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	snapshot, err := NewPostgresStore(db).PlatformSnapshot(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("platform snapshot: %v", err)
	}
	if snapshot.OutboxBacklog != 1 {
		t.Fatalf("outbox backlog = %d, want 1", snapshot.OutboxBacklog)
	}
	if snapshot.InboxExpiredLeases != 1 {
		t.Fatalf("expired inbox leases = %d, want 1", snapshot.InboxExpiredLeases)
	}
	if snapshot.ProjectionPendingRuns != 1 {
		t.Fatalf("pending projection runs = %d, want 1", snapshot.ProjectionPendingRuns)
	}
	if snapshot.ProjectionOldestPendingSeconds < 600 {
		t.Fatalf("projection lag = %f, want >= 600", snapshot.ProjectionOldestPendingSeconds)
	}
}
