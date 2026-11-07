package knowledgeprojection

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newInboxTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&models.TaskInbox{}); err != nil {
		t.Fatalf("migrate inbox: %v", err)
	}
	return db
}

func leaseProjectionTask(key string) Task {
	return Task{
		SchemaVersion: SchemaWikiTask, ProjectionRunID: "run", ProjectionType: ProjectionTypeWiki,
		Stage: StageCollect, IdempotencyKey: key,
	}
}

func TestProjectionClaimRejectsLiveLease(t *testing.T) {
	db := newInboxTestDB(t)
	task := leaseProjectionTask("run:collect")
	first := &Service{db: db}
	claimed, err := first.claimInbox(context.Background(), task)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	var before models.TaskInbox
	if err := db.First(&before, "idempotency_key = ?", task.IdempotencyKey).Error; err != nil {
		t.Fatalf("load inbox: %v", err)
	}
	if before.LeaseOwner == "" || before.LeaseUntil == nil || !before.LeaseUntil.After(time.Now()) {
		t.Fatalf("first claim must record a live lease, got owner=%q until=%v", before.LeaseOwner, before.LeaseUntil)
	}

	claimed, err = (&Service{db: db}).claimInbox(context.Background(), task)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("a live projection lease must not be claimed twice")
	}
	var after models.TaskInbox
	if err := db.First(&after, "idempotency_key = ?", task.IdempotencyKey).Error; err != nil {
		t.Fatalf("reload inbox: %v", err)
	}
	if after.LeaseOwner != before.LeaseOwner {
		t.Fatalf("rejected claim changed the owner: %q -> %q", before.LeaseOwner, after.LeaseOwner)
	}
}

func TestProjectionClaimTakesOverExpiredLease(t *testing.T) {
	db := newInboxTestDB(t)
	task := leaseProjectionTask("run:collect")
	first := &Service{db: db}
	if claimed, err := first.claimInbox(context.Background(), task); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	expired := time.Now().Add(-time.Minute)
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", task.IdempotencyKey).
		Updates(map[string]any{"lease_until": &expired}).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	claimed, err := (&Service{db: db}).claimInbox(context.Background(), task)
	if err != nil || !claimed {
		t.Fatalf("expired lease must be claimable: %v, %v", claimed, err)
	}
}

func TestProjectionClaimNeverClaimsSucceeded(t *testing.T) {
	future := time.Now().Add(time.Hour)
	expired := time.Now().Add(-time.Hour)
	cases := map[string]*time.Time{
		"live lease":    &future,
		"expired lease": &expired,
		"no lease":      nil,
	}
	for name, leaseUntil := range cases {
		t.Run(name, func(t *testing.T) {
			db := newInboxTestDB(t)
			task := leaseProjectionTask("run:collect")
			now := time.Now()
			if err := db.Create(&models.TaskInbox{
				IdempotencyKey: task.IdempotencyKey, TaskType: "projection.wiki.collect", Status: inboxSucceeded,
				LeaseUntil: leaseUntil, ProcessedAt: &now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed inbox: %v", err)
			}
			claimed, err := (&Service{db: db}).claimInbox(context.Background(), task)
			if err != nil {
				t.Fatalf("claim succeeded row: %v", err)
			}
			if claimed {
				t.Fatal("SUCCEEDED projection inbox must never be claimed again")
			}
		})
	}
}

func TestProjectionClaimElectsSingleOwner(t *testing.T) {
	db := newInboxTestDB(t)
	task := leaseProjectionTask("run:collect")
	workers := 8
	var wait sync.WaitGroup
	results := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, err := (&Service{db: db}).claimInbox(context.Background(), task)
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
}
