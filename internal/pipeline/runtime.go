// Package pipeline owns the queue runtime shared by ingestion and projection:
// inbox claim, execution lease, and the transactional stage/inbox/outbox
// updates. Each domain supplies a Stage that knows its rows and state machine.
package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/inboxlease"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const (
	InboxProcessing = inboxlease.StatusProcessing
	InboxSucceeded  = inboxlease.StatusSucceeded
	InboxFailed     = "FAILED"
)

// Stage is the domain half of one queue stage. Every method runs inside the
// transaction Runtime opens, so the stage rows, the inbox outcome and the next
// outbox event commit or roll back together.
type Stage interface {
	// Start records that the stage is running and validates the transition.
	Start(tx *gorm.DB, now time.Time) error
	// Succeed records the stage outcome. A non-nil event is the next stage's
	// outbox event; nil ends the chain.
	Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error)
	// Fail records a failed stage attempt.
	Fail(tx *gorm.DB, code, message string, now time.Time) error
}

type Runtime struct {
	db    *gorm.DB
	owner *inboxlease.Owner
	ttl   time.Duration
}

func NewRuntime(db *gorm.DB, owner *inboxlease.Owner, ttl time.Duration) *Runtime {
	if owner == nil {
		owner = &inboxlease.Owner{}
	}
	return &Runtime{db: db, owner: owner, ttl: ttl}
}

func (r *Runtime) lease() inboxlease.Lease {
	return inboxlease.New(r.owner.Get(), r.ttl)
}

// Claim elects exactly one owner for the inbox row, or reports false when
// another worker holds a live lease.
func (r *Runtime) Claim(ctx context.Context, key, taskType string, stageRunID uint64) (bool, error) {
	return inboxlease.Claim(ctx, r.db, key, taskType, stageRunID, r.lease())
}

// Heartbeat renews the lease while the stage runs. The returned context is
// canceled when the lease is lost, release always drops this owner's lease, and
// lost reports whether another worker may already own the row.
func (r *Runtime) Heartbeat(ctx context.Context, key string) (context.Context, func(), func() bool) {
	lease := r.lease()
	leaseCtx, cancel := context.WithCancel(ctx)
	var lost atomic.Bool
	stop := inboxlease.Keep(leaseCtx, r.db, key, lease, func() {
		lost.Store(true)
		cancel()
	})
	release := func() {
		stop()
		cancel()
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		if err := inboxlease.Release(releaseCtx, r.db, key, lease.Owner); err != nil {
			slog.Warn("inbox_lease_release_failed", "idempotency_key", key, "error", err)
		}
	}
	return leaseCtx, release, lost.Load
}

func (r *Runtime) Start(ctx context.Context, stage Stage) error {
	now := time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return stage.Start(tx, now)
	})
}

// Complete commits the stage outcome, the inbox SUCCEEDED row and, when the
// domain returns one, the next stage's event in a single transaction.
func (r *Runtime) Complete(ctx context.Context, key string, stage Stage) error {
	now := time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		next, err := stage.Succeed(tx, now)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Updates(map[string]any{
			"status": InboxSucceeded, "processed_at": &now, "lease_owner": "", "lease_until": nil, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if next == nil {
			return nil
		}
		return ReopenEvent(tx, *next)
	})
}

func (r *Runtime) Fail(ctx context.Context, stage Stage, code, message string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return stage.Fail(tx, code, message, now)
	})
}

func (r *Runtime) FinishInbox(ctx context.Context, key, status string, processErr error) error {
	now := time.Now()
	values := map[string]any{"status": status, "lease_owner": "", "lease_until": nil, "updated_at": now}
	if status == InboxSucceeded || status == InboxFailed {
		values["processed_at"] = now
	}
	if processErr != nil {
		values["last_error"] = processErr.Error()
	}
	return r.db.WithContext(ctx).Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Updates(values).Error
}
