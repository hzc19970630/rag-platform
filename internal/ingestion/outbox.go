package ingestion

import (
	"context"

	"github.com/example/rag-platform/internal/platform/outbox"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// OutboxPublisher drains document.* events into the ingestion queue. Claim,
// backoff and malformed-payload handling live in platform/outbox so all queues
// share one retry policy.
type OutboxPublisher = outbox.Publisher

func NewOutboxPublisher(db *gorm.DB, publisher TaskPublisher, maxAttempts, batchSize int) *OutboxPublisher {
	return outbox.New(outbox.Options{
		DB:          db,
		MaxAttempts: maxAttempts,
		BatchSize:   batchSize,
		Label:       "publish outbox event",
		// Connector sync events share the outbox table but are dispatched by the
		// datasource publisher; claiming them here would deserialize a sync
		// payload as a stage task.
		Filter:     "event_type LIKE ?",
		FilterArgs: []any{"document.%"},
		Decode:     outbox.DecodeJSON[Task](),
		Publish: func(ctx context.Context, _ models.OutboxEvent, task any) error {
			return publisher.Publish(ctx, task.(Task))
		},
	})
}
