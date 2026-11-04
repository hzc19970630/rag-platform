package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const (
	DeadLetterPending   = "PENDING"
	DeadLetterRetrying  = "RETRYING"
	DeadLetterResolved  = "RESOLVED"
	DeadLetterDiscarded = "DISCARDED"
)

// UpsertDeadLetter records a task whose retries are exhausted. Both the queue
// error handler and reconcile may observe the same failure, so the write is
// idempotent on the stage key. A resolved letter stays resolved.
func UpsertDeadLetter(tx *gorm.DB, row models.TaskDeadLetter) error {
	var existing models.TaskDeadLetter
	err := tx.Where("idempotency_key = ?", row.IdempotencyKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row.Status = DeadLetterPending
		return tx.Create(&row).Error
	}
	if err != nil {
		return err
	}
	if existing.Status == DeadLetterResolved {
		return nil
	}
	return tx.Model(&existing).Updates(map[string]any{
		"attempts": row.Attempts, "last_error": row.LastError, "payload": row.Payload,
		"status": DeadLetterPending, "updated_at": time.Now(),
	}).Error
}

// FailProcessingInbox releases the inbox lease as FAILED only while the row is
// still PROCESSING. A stage outcome written by the lease owner wins.
func FailProcessingInbox(ctx context.Context, db *gorm.DB, key, message string) error {
	now := time.Now()
	return db.WithContext(ctx).Model(&models.TaskInbox{}).
		Where("idempotency_key = ? AND status = ?", key, InboxProcessing).
		Updates(map[string]any{
			"status": InboxFailed, "last_error": message, "lease_owner": "", "lease_until": nil, "updated_at": now,
		}).Error
}
