// Package outbox drains the transactional outbox with one implementation of
// claim, publish, backoff and malformed-payload handling. Each domain supplies
// its event filter and decoder, so retry semantics cannot drift per queue.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Options struct {
	DB          *gorm.DB
	MaxAttempts int
	// BatchSize is how many events one tick claims. Claiming a batch instead of
	// one row is what lets worker replicas share the outbox without a 500ms
	// per-event ceiling.
	BatchSize int
	// Lease marks claimed rows invisible to other publishers while this process
	// publishes them. A crashed publisher retries after the lease expires.
	Lease time.Duration
	// Label prefixes wrapped errors, e.g. "publish connector sync event".
	Label string
	// Filter is a WHERE fragment selecting this publisher's events. It runs in
	// the same claim as the published/attempt/backoff predicates.
	Filter     string
	FilterArgs []any
	// Decode parses the payload. A decode failure parks the event at the attempt
	// limit because no retry can succeed.
	Decode func([]byte) (any, error)
	// Publish delivers the decoded task. It runs after the claim transaction
	// commits, so a slow Redis call never holds a database row lock.
	Publish  func(context.Context, models.OutboxEvent, any) error
	Interval time.Duration
}

type Publisher struct {
	options Options
	// Run is the only caller, so the error log can be throttled without a lock.
	lastLogAt time.Time
	lastError string
}

func New(options Options) *Publisher {
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 10
	}
	if options.Interval <= 0 {
		options.Interval = 500 * time.Millisecond
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 64
	}
	if options.Lease <= 0 {
		options.Lease = 60 * time.Second
	}
	if options.Filter == "" {
		options.Filter = "1=1"
	}
	return &Publisher{options: options}
}

// Run publishes one batch per tick until the context is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.publishBatch(ctx); err != nil {
				now := time.Now()
				if err.Error() != p.lastError || now.Sub(p.lastLogAt) >= time.Minute {
					slog.Warn("outbox_publish_failed", "label", p.options.Label, "error", err)
					p.lastLogAt = now
					p.lastError = err.Error()
				}
			} else {
				p.lastError = ""
			}
		}
	}
}

// PublishPending drains everything currently eligible and returns the first
// error. It is used by recovery and tests.
func (p *Publisher) PublishPending(ctx context.Context) error {
	for {
		published, err := p.publishBatch(ctx)
		if err != nil || published == 0 {
			return err
		}
	}
}

// claimBatch selects and leases a batch inside one transaction. Claiming does
// not increment attempts; a failed publish does. The lease keeps another
// publisher from taking the same rows while this one talks to Redis.
func (p *Publisher) claimBatch(ctx context.Context) ([]models.OutboxEvent, error) {
	var events []models.OutboxEvent
	now := time.Now()
	err := p.options.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		args := append([]any{}, p.options.FilterArgs...)
		args = append(args, p.options.MaxAttempts, now)
		query := tx.Where(p.options.Filter+` AND published_at IS NULL AND attempts < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)`, args...)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := query.Order("id").Limit(p.options.BatchSize).Find(&events).Error; err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		return tx.Model(&models.OutboxEvent{}).Where("id IN ?", ids).Update("next_attempt_at", now.Add(p.options.Lease)).Error
	})
	if err != nil {
		return nil, fmt.Errorf("%s: claim batch: %w", p.options.Label, err)
	}
	return events, nil
}

// publishBatch claims a batch, publishes outside the transaction, then records
// the outcome. At-least-once delivery is safe: ingestion/sync/projection tasks
// carry an idempotency key that Asynq and task_inbox already deduplicate.
func (p *Publisher) publishBatch(ctx context.Context) (int, error) {
	events, err := p.claimBatch(ctx)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}
	now := time.Now()
	publishedIDs := make([]uint64, 0, len(events))
	for _, event := range events {
		task, decodeErr := p.options.Decode([]byte(event.Payload))
		if decodeErr != nil {
			if markErr := p.options.DB.WithContext(ctx).Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
				"attempts": p.options.MaxAttempts, "last_error": "malformed payload: " + decodeErr.Error(),
			}).Error; markErr != nil {
				return len(publishedIDs), fmt.Errorf("%s: park malformed event %d: %w", p.options.Label, event.ID, markErr)
			}
			continue
		}
		if publishErr := p.options.Publish(ctx, event, task); publishErr != nil {
			attempts := event.Attempts + 1
			backoff := time.Duration(1<<min(attempts, 6)) * time.Second
			next := now.Add(backoff)
			if markErr := p.options.DB.WithContext(ctx).Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
				"attempts": attempts, "next_attempt_at": &next, "last_error": publishErr.Error(),
			}).Error; markErr != nil {
				return len(publishedIDs), fmt.Errorf("%s: record failure for event %d: %w", p.options.Label, event.ID, markErr)
			}
			continue
		}
		publishedIDs = append(publishedIDs, event.ID)
	}
	if len(publishedIDs) > 0 {
		if err := p.options.DB.WithContext(ctx).Model(&models.OutboxEvent{}).Where("id IN ?", publishedIDs).Updates(map[string]any{
			"published_at": &now, "attempts": gorm.Expr("attempts + 1"), "last_error": "", "next_attempt_at": nil,
		}).Error; err != nil {
			return len(publishedIDs), fmt.Errorf("%s: mark published: %w", p.options.Label, err)
		}
	}
	return len(publishedIDs), nil
}

// DecodeJSON adapts a typed decoder to Options.Decode.
func DecodeJSON[T any]() func([]byte) (any, error) {
	return func(payload []byte) (any, error) {
		var value T
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		return value, nil
	}
}
