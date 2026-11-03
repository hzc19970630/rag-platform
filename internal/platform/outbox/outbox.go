// Package outbox drains the transactional outbox with one implementation of
// claim, publish, backoff and malformed-payload handling. Each domain supplies
// its event filter and decoder, so retry semantics cannot drift per queue.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Options struct {
	DB          *gorm.DB
	MaxAttempts int
	// Label prefixes wrapped errors, e.g. "publish connector sync event".
	Label string
	// Filter is a WHERE fragment selecting this publisher's events. It runs in
	// the same claim as the published/attempt/backoff predicates.
	Filter     string
	FilterArgs []any
	// Decode parses the payload. A decode failure parks the event at the attempt
	// limit because no retry can succeed.
	Decode func([]byte) (any, error)
	// Publish delivers the decoded task. It runs inside the claim transaction.
	Publish  func(context.Context, models.OutboxEvent, any) error
	Interval time.Duration
}

type Publisher struct {
	options Options
}

func New(options Options) *Publisher {
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 10
	}
	if options.Interval <= 0 {
		options.Interval = 500 * time.Millisecond
	}
	return &Publisher{options: options}
}

// Run drains one event per tick until the context is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = p.publishOne(ctx)
		}
	}
}

// PublishPending drains everything currently eligible and returns the first
// error. It is used by recovery and tests.
func (p *Publisher) PublishPending(ctx context.Context) error {
	for {
		published, err := p.publishOne(ctx)
		if err != nil || !published {
			return err
		}
	}
}

func (p *Publisher) publishOne(ctx context.Context) (bool, error) {
	published := false
	err := p.options.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event models.OutboxEvent
		now := time.Now()
		args := append([]any{}, p.options.FilterArgs...)
		args = append(args, p.options.MaxAttempts, now)
		result := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(p.options.Filter+` AND published_at IS NULL AND attempts < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)`, args...).
			Order("id").Limit(1).Find(&event)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		task, err := p.options.Decode([]byte(event.Payload))
		if err != nil {
			return tx.Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
				"attempts": p.options.MaxAttempts, "last_error": "malformed payload: " + err.Error(),
			}).Error
		}
		if err := p.options.Publish(ctx, event, task); err != nil {
			attempts := event.Attempts + 1
			backoff := time.Duration(1<<min(attempts, 6)) * time.Second
			next := now.Add(backoff)
			return tx.Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
				"attempts": attempts, "next_attempt_at": &next, "last_error": err.Error(),
			}).Error
		}
		if err := tx.Model(&models.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
			"published_at": &now, "attempts": event.Attempts + 1, "last_error": "", "next_attempt_at": nil,
		}).Error; err != nil {
			return err
		}
		published = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("%s: %w", p.options.Label, err)
	}
	return published, nil
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
