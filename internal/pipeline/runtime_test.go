package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
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
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.TaskInbox{}, &models.OutboxEvent{}, &models.TaskDeadLetter{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

type fakeStage struct {
	started, succeeded, failed int
	next                       *models.OutboxEvent
	succeedErr                 error
}

func (f *fakeStage) Start(tx *gorm.DB, now time.Time) error {
	f.started++
	return nil
}

func (f *fakeStage) Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error) {
	f.succeeded++
	if f.succeedErr != nil {
		return nil, f.succeedErr
	}
	return f.next, nil
}

func (f *fakeStage) Fail(tx *gorm.DB, code, message string, now time.Time) error {
	f.failed++
	return nil
}

func loadInbox(t *testing.T, db *gorm.DB, key string) models.TaskInbox {
	t.Helper()
	var row models.TaskInbox
	if err := db.First(&row, "idempotency_key = ?", key).Error; err != nil {
		t.Fatalf("load inbox %s: %v", key, err)
	}
	return row
}

func TestRuntimeClaimElectsSingleOwner(t *testing.T) {
	db := newTestDB(t)
	workers := 8
	var wait sync.WaitGroup
	results := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, err := NewRuntime(db, nil, time.Minute).Claim(context.Background(), "run:parse", "ingestion", 1)
			if err != nil {
				t.Errorf("claim: %v", err)
				results <- false
				return
			}
			results <- claimed
		}()
	}
	wait.Wait()
	close(results)
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one claim winner, got %d", winners)
	}
	row := loadInbox(t, db, "run:parse")
	if row.Status != InboxProcessing || row.LeaseOwner == "" {
		t.Fatalf("claimed inbox = %+v", row)
	}
}

func TestRuntimeHeartbeatReleasesLease(t *testing.T) {
	db := newTestDB(t)
	runtime := NewRuntime(db, nil, 50*time.Millisecond)
	if claimed, err := runtime.Claim(context.Background(), "run:parse", "ingestion", 1); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	_, release, lost := runtime.Heartbeat(context.Background(), "run:parse")
	if lost() {
		t.Fatal("a fresh lease must not be reported lost")
	}
	release()
	row := loadInbox(t, db, "run:parse")
	if row.LeaseOwner != "" || row.LeaseUntil != nil {
		t.Fatalf("release must clear the lease, got owner=%q until=%v", row.LeaseOwner, row.LeaseUntil)
	}
	if row.Status != InboxProcessing {
		t.Fatalf("release must not change status, got %q", row.Status)
	}
}

func TestRuntimeHeartbeatReportsLostLease(t *testing.T) {
	db := newTestDB(t)
	runtime := NewRuntime(db, nil, 30*time.Millisecond)
	if claimed, err := runtime.Claim(context.Background(), "run:parse", "ingestion", 1); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	expired := time.Now().Add(-time.Minute)
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", "run:parse").
		Updates(map[string]any{"lease_owner": "another-worker", "lease_until": &expired}).Error; err != nil {
		t.Fatalf("steal lease: %v", err)
	}
	ctx, release, lost := runtime.Heartbeat(context.Background(), "run:parse")
	defer release()
	deadline := time.After(2 * time.Second)
	for !lost() {
		select {
		case <-deadline:
			t.Fatal("heartbeat did not report the stolen lease")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lost lease must cancel the process context")
	}
	if row := loadInbox(t, db, "run:parse"); row.LeaseOwner != "another-worker" {
		t.Fatalf("release must not touch another owner's lease, got %q", row.LeaseOwner)
	}
}

func TestRuntimeCompleteCommitsInboxAndOutbox(t *testing.T) {
	db := newTestDB(t)
	runtime := NewRuntime(db, nil, time.Minute)
	if claimed, err := runtime.Claim(context.Background(), "run:parse", "ingestion", 1); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	stage := &fakeStage{next: &models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.normalize", IdempotencyKey: "run:normalize", Payload: `{"stage":"normalize"}`,
	}}
	if err := runtime.Complete(context.Background(), "run:parse", stage); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if stage.started != 0 || stage.succeeded != 1 || stage.failed != 0 {
		t.Fatalf("hook calls = start:%d succeed:%d fail:%d", stage.started, stage.succeeded, stage.failed)
	}
	inbox := loadInbox(t, db, "run:parse")
	if inbox.Status != InboxSucceeded || inbox.ProcessedAt == nil || inbox.LeaseOwner != "" || inbox.LeaseUntil != nil {
		t.Fatalf("completed inbox = %+v", inbox)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", "run:normalize").Error; err != nil {
		t.Fatalf("load outbox event: %v", err)
	}
	if event.Payload != `{"stage":"normalize"}` || event.PublishedAt != nil || event.EventType != "document.normalize" {
		t.Fatalf("outbox event = %+v", event)
	}
}

func TestRuntimeCompleteReopensPublishedEvent(t *testing.T) {
	db := newTestDB(t)
	published := time.Now()
	if err := db.Create(&models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.normalize", IdempotencyKey: "run:normalize", Payload: `{"stage":"stale"}`,
		Attempts: 3, PublishedAt: &published, LastError: "old",
	}).Error; err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	runtime := NewRuntime(db, nil, time.Minute)
	stage := &fakeStage{next: &models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.normalize", IdempotencyKey: "run:normalize", Payload: `{"stage":"normalize"}`,
	}}
	if err := runtime.Complete(context.Background(), "run:parse", stage); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", "run:normalize").Error; err != nil {
		t.Fatalf("load outbox event: %v", err)
	}
	if event.PublishedAt != nil || event.Attempts != 0 || event.LastError != "" {
		t.Fatalf("reopened event = %+v", event)
	}
	if event.Payload != `{"stage":"normalize"}` {
		t.Fatalf("reopened payload = %q", event.Payload)
	}
}

func TestRuntimeCompleteRollsBackOnHookFailure(t *testing.T) {
	db := newTestDB(t)
	runtime := NewRuntime(db, nil, time.Minute)
	if claimed, err := runtime.Claim(context.Background(), "run:parse", "ingestion", 1); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	stage := &failingStage{}
	if err := runtime.Complete(context.Background(), "run:parse", stage); err == nil {
		t.Fatal("expected the hook failure to abort completion")
	}
	inbox := loadInbox(t, db, "run:parse")
	if inbox.Status != InboxProcessing || inbox.ProcessedAt != nil {
		t.Fatalf("failed completion must leave the inbox PROCESSING, got %+v", inbox)
	}
	var count int64
	if err := db.Model(&models.OutboxEvent{}).Where("idempotency_key = ?", "run:normalize").Count(&count).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed completion must not enqueue the next stage, found %d events", count)
	}
}

type failingStage struct{ fakeStage }

func (f *failingStage) Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error) {
	return nil, errors.New("stage write failed")
}

func TestRuntimeFailUsesHookTransaction(t *testing.T) {
	db := newTestDB(t)
	if err := db.Create(&models.TaskInbox{IdempotencyKey: "run:parse", TaskType: "ingestion", Status: InboxProcessing, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	runtime := NewRuntime(db, nil, time.Minute)
	stage := &fakeStage{}
	if err := runtime.Fail(context.Background(), stage, "parser_error", "bad bytes"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if stage.failed != 1 {
		t.Fatalf("fail hook calls = %d", stage.failed)
	}
	if row := loadInbox(t, db, "run:parse"); row.Status != InboxProcessing {
		t.Fatalf("Fail must not finalize the inbox itself, got %q", row.Status)
	}
}

func TestFinishInboxReportsStatusAndError(t *testing.T) {
	db := newTestDB(t)
	runtime := NewRuntime(db, nil, time.Minute)
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: "run:parse", TaskType: "ingestion", Status: InboxProcessing,
		LeaseOwner: "worker-a", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	if err := runtime.FinishInbox(context.Background(), "run:parse", InboxFailed, errors.New("terminal")); err != nil {
		t.Fatalf("finish inbox: %v", err)
	}
	row := loadInbox(t, db, "run:parse")
	if row.Status != InboxFailed || row.ProcessedAt == nil || row.LastError != "terminal" || row.LeaseOwner != "" {
		t.Fatalf("failed inbox = %+v", row)
	}
}

func TestInboxRecoveryHelpersShareTheTerminalShape(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: "run:parse", TaskType: "ingestion", Status: InboxSucceeded,
		LeaseOwner: "worker-a", ProcessedAt: &now, LastError: "old failure", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}

	if err := RequeueInbox(db, "run:parse", now); err != nil {
		t.Fatalf("requeue inbox: %v", err)
	}
	row := loadInbox(t, db, "run:parse")
	if row.Status != InboxFailed || row.ProcessedAt != nil || row.LeaseOwner != "" || row.LeaseUntil != nil || row.LastError != "" {
		t.Fatalf("requeued inbox = %+v", row)
	}

	if err := SettleInbox(db, "run:parse", InboxSucceeded, now); err != nil {
		t.Fatalf("settle inbox: %v", err)
	}
	row = loadInbox(t, db, "run:parse")
	if row.Status != InboxSucceeded || row.ProcessedAt == nil || row.LeaseOwner != "" || row.LastError != "" {
		t.Fatalf("settled inbox = %+v", row)
	}
}
