package knowledgeprojection

import (
	"context"

	"github.com/example/rag-platform/internal/platform/outbox"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// OutboxPublisher drains projection.wiki and projection.relation events into
// the projection queue pool.
type OutboxPublisher = outbox.Publisher

func NewOutboxPublisher(db *gorm.DB, publisher Publisher, maxAttempts, batchSize int) *OutboxPublisher {
	return outbox.New(outbox.Options{
		DB:          db,
		MaxAttempts: maxAttempts,
		BatchSize:   batchSize,
		Label:       "publish projection event",
		Filter:      "event_type IN ?",
		FilterArgs:  []any{[]string{"projection.wiki", "projection.relation"}},
		Decode:      outbox.DecodeJSON[Task](),
		Publish: func(ctx context.Context, event models.OutboxEvent, task any) error {
			if event.EventType == "projection.relation" {
				return publisher.PublishRelation(ctx, task.(Task))
			}
			return publisher.PublishWiki(ctx, task.(Task))
		},
	})
}
