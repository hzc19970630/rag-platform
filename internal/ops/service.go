// Package ops owns operator-facing snapshots built from durable tables. The
// HTTP transport stays thin: it authenticates, calls this service and serializes.
package ops

import (
	"context"
	"time"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

type Snapshot struct {
	Stages           map[string]map[string]int64 `json:"stages"`
	ParseFailureRate float64                     `json:"parseFailureRate"`
	ModelErrors24h   int64                       `json:"modelErrors24h"`
	DeadLetters      map[string]int64            `json:"deadLetters"`
	OutboxBacklog    int64                       `json:"outboxBacklog"`
}

func (s *Service) Snapshot(ctx context.Context, tenantID string, since time.Time, outboxMaxAttempts int) (Snapshot, error) {
	return s.store.Snapshot(ctx, tenantID, since, outboxMaxAttempts)
}
