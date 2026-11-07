package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// EventStore is the persistence port for the resumable SSE event log.
type EventStore interface {
	Replay(ctx context.Context, tenantID, userID, requestID string, afterSequence int) ([]models.StreamEvent, error)
	Append(ctx context.Context, event models.StreamEvent) error
	Purge(ctx context.Context, before time.Time, limit int) (int64, error)
}

type PostgresEventStore struct {
	db *gorm.DB
}

func NewPostgresEventStore(db *gorm.DB) *PostgresEventStore {
	return &PostgresEventStore{db: db}
}

func (s *PostgresEventStore) Replay(ctx context.Context, tenantID, userID, requestID string, afterSequence int) ([]models.StreamEvent, error) {
	var rows []models.StreamEvent
	// Enforce mode needs one tenant-scoped transaction per replay: a pooled
	// statement cannot carry app.tenant_id for the next query. InTx joins the
	// request transaction when one is still active.
	err := tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		var message models.Message
		if err := tenantdb.MustScoped(tx, tenantID).
			Where("user_id = ? AND request_id = ?", userID, requestID).First(&message).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperror.ErrNotFound
			}
			return fmt.Errorf("load request messages: %w", err)
		}
		if err := tenantdb.MustScoped(tx, tenantID).
			Where("user_id = ? AND request_id = ? AND sequence > ?", userID, requestID, afterSequence).Order("sequence").Find(&rows).Error; err != nil {
			return fmt.Errorf("load stream events: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *PostgresEventStore) Append(ctx context.Context, event models.StreamEvent) error {
	if err := s.db.WithContext(ctx).Create(&event).Error; err != nil {
		return fmt.Errorf("persist stream event: %w", err)
	}
	return nil
}

func (s *PostgresEventStore) Purge(ctx context.Context, before time.Time, limit int) (int64, error) {
	result := tenantdb.Unscoped(s.db.WithContext(ctx), "retention: purge expired stream events across tenants").Exec(`
		DELETE FROM stream_events
		WHERE id IN (
			SELECT id FROM stream_events WHERE created_at < ? ORDER BY created_at, id LIMIT ?
		)`, before, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("purge stream events: %w", result.Error)
	}
	return result.RowsAffected, nil
}
