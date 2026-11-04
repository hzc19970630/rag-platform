package datasource

import (
	"context"

	"github.com/example/rag-platform/internal/platform/outbox"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// SyncPublisher is implemented by the queue publisher. The datasource package
// deliberately does not import the queue package, so task transport stays a
// platform concern.
type SyncPublisher interface {
	PublishSync(ctx context.Context, task SyncTask) error
}

// OutboxPublisher drains connector.sync events into the maintenance queue.
type OutboxPublisher = outbox.Publisher

func NewOutboxPublisher(db *gorm.DB, publisher SyncPublisher, maxAttempts, batchSize int) *OutboxPublisher {
	return outbox.New(outbox.Options{
		DB:          db,
		MaxAttempts: maxAttempts,
		BatchSize:   batchSize,
		Label:       "publish connector sync event",
		Filter:      "event_type = ?",
		FilterArgs:  []any{"connector.sync"},
		Decode:      outbox.DecodeJSON[SyncTask](),
		Publish: func(ctx context.Context, _ models.OutboxEvent, task any) error {
			return publisher.PublishSync(ctx, task.(SyncTask))
		},
	})
}
