package pipeline

import (
	"errors"
	"fmt"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// ReopenEvent makes event publishable again: it creates the row when absent and
// otherwise rewrites its payload and clears publication state.
func ReopenEvent(tx *gorm.DB, event models.OutboxEvent) error {
	var existing models.OutboxEvent
	err := tx.Where("idempotency_key = ?", event.IdempotencyKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&event).Error
	}
	if err != nil {
		return err
	}
	if existing.TenantID != event.TenantID {
		return fmt.Errorf("outbox event %s belongs to another tenant", event.IdempotencyKey)
	}
	return tx.Model(&existing).Updates(map[string]any{
		"payload": event.Payload, "published_at": nil, "attempts": 0, "next_attempt_at": nil, "last_error": "",
	}).Error
}

// RequeueEvent reports whether the event needs publishing now. An existing
// event that was never published is left alone: that delivery is still pending,
// and reopening it would count the same work twice.
func RequeueEvent(tx *gorm.DB, event models.OutboxEvent) (bool, error) {
	var existing models.OutboxEvent
	err := tx.Where("idempotency_key = ?", event.IdempotencyKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, tx.Create(&event).Error
	}
	if err != nil {
		return false, err
	}
	if existing.TenantID != event.TenantID {
		return false, fmt.Errorf("outbox event %s belongs to another tenant", event.IdempotencyKey)
	}
	if existing.PublishedAt == nil {
		return false, nil
	}
	return true, tx.Model(&existing).Updates(map[string]any{
		"published_at": nil, "attempts": 0, "next_attempt_at": nil, "last_error": "",
	}).Error
}
