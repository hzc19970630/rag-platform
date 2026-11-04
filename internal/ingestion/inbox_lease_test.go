package ingestion

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
)

func leaseTask(key string) Task {
	return Task{
		Envelope: Envelope{StageRunID: "1", IdempotencyKey: key},
		TaskType: TypeTask, Stage: StageParse,
	}
}

func loadInbox(t *testing.T, worker *PipelineWorker, key string) models.TaskInbox {
	t.Helper()
	var row models.TaskInbox
	if err := worker.db.First(&row, "idempotency_key = ?", key).Error; err != nil {
		t.Fatalf("load inbox %s: %v", key, err)
	}
	return row
}

func TestClaimInboxRejectsLiveLease(t *testing.T) {
	db := newTestDB(t)
	task := leaseTask("run:parse")
	first := &PipelineWorker{db: db}
	claimed, err := first.claimInbox(context.Background(), task)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	before := loadInbox(t, first, task.IdempotencyKey)
	if before.LeaseOwner == "" || before.LeaseUntil == nil || !before.LeaseUntil.After(time.Now()) {
		t.Fatalf("first claim must record a live lease, got owner=%q until=%v", before.LeaseOwner, before.LeaseUntil)
	}

	second := &PipelineWorker{db: db}
	claimed, err = second.claimInbox(context.Background(), task)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("a live lease must not be claimed by a second worker")
	}
	after := loadInbox(t, first, task.IdempotencyKey)
	if after.LeaseOwner != before.LeaseOwner || after.LeaseUntil == nil || !after.LeaseUntil.Equal(*before.LeaseUntil) {
		t.Fatalf("rejected claim modified the lease: owner %q -> %q, until %v -> %v", before.LeaseOwner, after.LeaseOwner, before.LeaseUntil, after.LeaseUntil)
	}
}

func TestClaimInboxTakesOverExpiredLease(t *testing.T) {
	db := newTestDB(t)
	task := leaseTask("run:parse")
	first := &PipelineWorker{db: db}
	if claimed, err := first.claimInbox(context.Background(), task); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	before := loadInbox(t, first, task.IdempotencyKey)
	expired := time.Now().Add(-time.Minute)
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", task.IdempotencyKey).
		Updates(map[string]any{"lease_until": &expired}).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	second := &PipelineWorker{db: db}
	claimed, err := second.claimInbox(context.Background(), task)
	if err != nil {
		t.Fatalf("take over expired lease: %v", err)
	}
	if !claimed {
		t.Fatal("an expired lease must be claimable")
	}
	after := loadInbox(t, first, task.IdempotencyKey)
	if after.LeaseOwner == before.LeaseOwner {
		t.Fatalf("takeover must replace the owner, still %q", after.LeaseOwner)
	}
	if after.LeaseUntil == nil || !after.LeaseUntil.After(time.Now()) {
		t.Fatalf("takeover must install a new live lease, got %v", after.LeaseUntil)
	}
}

func TestClaimInboxNeverClaimsSucceeded(t *testing.T) {
	future := time.Now().Add(time.Hour)
	expired := time.Now().Add(-time.Hour)
	cases := map[string]*time.Time{
		"live lease":    &future,
		"expired lease": &expired,
		"no lease":      nil,
	}
	for name, leaseUntil := range cases {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			task := leaseTask("run:parse")
			now := time.Now()
			if err := db.Create(&models.TaskInbox{
				IdempotencyKey: task.IdempotencyKey, TaskType: TypeTask, Status: inboxSucceeded,
				LeaseOwner: "worker-a", LeaseUntil: leaseUntil, ProcessedAt: &now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed inbox: %v", err)
			}
			claimed, err := (&PipelineWorker{db: db}).claimInbox(context.Background(), task)
			if err != nil {
				t.Fatalf("claim succeeded row: %v", err)
			}
			if claimed {
				t.Fatal("SUCCEEDED inbox must never be claimed again")
			}
		})
	}
}

func TestClaimInboxClaimsReopenedRow(t *testing.T) {
	for _, status := range []string{inboxFailed, StagePending} {
		t.Run(status, func(t *testing.T) {
			db := newTestDB(t)
			task := leaseTask("run:parse")
			now := time.Now()
			if err := db.Create(&models.TaskInbox{
				IdempotencyKey: task.IdempotencyKey, TaskType: TypeTask, Status: status,
				CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed inbox: %v", err)
			}
			claimed, err := (&PipelineWorker{db: db}).claimInbox(context.Background(), task)
			if err != nil || !claimed {
				t.Fatalf("reopened row must be claimable: %v, %v", claimed, err)
			}
			if row := loadInbox(t, &PipelineWorker{db: db}, task.IdempotencyKey); row.Status != inboxProcessing {
				t.Fatalf("status after claim = %q", row.Status)
			}
		})
	}
}

// Two workers delivered the same stage concurrently must elect exactly one
// owner; otherwise the stage work runs twice.
func TestClaimInboxElectsSingleOwner(t *testing.T) {
	db := newTestDB(t)
	task := leaseTask("run:parse")
	workers := 8
	var wait sync.WaitGroup
	results := make(chan bool, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, err := (&PipelineWorker{db: db}).claimInbox(context.Background(), task)
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
