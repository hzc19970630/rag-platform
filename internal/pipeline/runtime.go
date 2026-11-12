// Package pipeline owns the queue runtime shared by ingestion and projection:
// inbox claim, execution lease, and the transactional stage/inbox/outbox
// updates. Each domain supplies a Stage that knows its rows and state machine.
package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/inboxlease"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
)

// ErrLeaseRetained tells the queue handler that a failed attempt deliberately
// kept its inbox lease until the retry backoff expires. The task must be
// retried later instead of being acknowledged as a duplicate.
var ErrLeaseRetained = errors.New("inbox lease retained until retry backoff")

const (
	InboxProcessing = inboxlease.StatusProcessing
	InboxSucceeded  = inboxlease.StatusSucceeded
	InboxFailed     = status.Failed
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
// another worker holds a live lease. A failed attempt that retained its lease
// is reported as ErrLeaseRetained so the queue retries later instead of
// acknowledging the delivery as a duplicate.
func (r *Runtime) Claim(ctx context.Context, key, taskType string, stageRunID uint64) (bool, error) {
	claimed, err := inboxlease.Claim(ctx, r.db, key, taskType, stageRunID, r.lease())
	if err != nil || claimed {
		return claimed, err
	}
	var row models.TaskInbox
	if err := r.db.WithContext(ctx).Where("idempotency_key = ?", key).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if row.Status == InboxFailed && row.LeaseUntil != nil && row.LeaseUntil.After(time.Now()) {
		return false, ErrLeaseRetained
	}
	return false, nil
}

// Heartbeat renews the lease while the stage runs. The returned context is
// canceled when the lease is lost. release drops this owner's lease; retain
// stops the heartbeat but keeps the lease until its TTL, which is how a
// retryable failure honours the queue's retry backoff. lost reports whether
// another worker may already own the row.
func (r *Runtime) Heartbeat(ctx context.Context, key string) (context.Context, func(), func(), func() bool) {
	lease := r.lease()
	leaseCtx, cancel := context.WithCancel(ctx)
	var lost atomic.Bool
	stop := inboxlease.Keep(leaseCtx, r.db, key, lease, func() {
		lost.Store(true)
		cancel()
	})
	retain := func() {
		stop()
		cancel()
	}
	release := func() {
		retain()
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		if err := inboxlease.Release(releaseCtx, r.db, key, lease.Owner); err != nil {
			slog.Warn("inbox_lease_release_failed", "idempotency_key", key, "error", err)
		}
	}
	return leaseCtx, release, retain, lost.Load
}

// RetainFailedInbox marks a retryable failure while keeping the current lease
// intact. The next claim cannot start before the lease expires, so a fast retry
// cannot bypass the queue's exponential backoff.
func RetainFailedInbox(ctx context.Context, db *gorm.DB, key, message string) error {
	now := time.Now()
	return db.WithContext(ctx).Model(&models.TaskInbox{}).
		Where("idempotency_key = ? AND status = ?", key, InboxProcessing).
		Updates(map[string]any{
			"status": InboxFailed, "last_error": message, "updated_at": now,
		}).Error
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
	return r.db.WithContext(ctx).Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).
		Updates(inboxFinishValues(status, processErr, time.Now())).Error
}

// SettleInbox marks a row finished from a recovery path that repaired work
// without running the stage handler. It shares the terminal field set with
// FinishInbox so recovery and the worker cannot drift.
func SettleInbox(tx *gorm.DB, key, status string, now time.Time) error {
	return tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).
		Updates(inboxFinishValues(status, nil, now)).Error
}

// RequeueInbox makes a finished inbox row claimable again. A retry is not a
// success: the row stays FAILED, but cleared processed_at and lease fields let
// the next attempt claim it.
func RequeueInbox(tx *gorm.DB, key string, now time.Time) error {
	return tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Updates(map[string]any{
		"status": InboxFailed, "processed_at": nil, "last_error": "",
		"lease_owner": "", "lease_until": nil, "updated_at": now,
	}).Error
}

func inboxFinishValues(status string, processErr error, now time.Time) map[string]any {
	values := map[string]any{"status": status, "lease_owner": "", "lease_until": nil, "updated_at": now}
	if status == InboxSucceeded || status == InboxFailed {
		values["processed_at"] = now
	}
	if status == InboxSucceeded {
		values["last_error"] = ""
	}
	if processErr != nil {
		values["last_error"] = processErr.Error()
	}
	return values
}
