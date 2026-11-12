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

// PlatformSnapshot is the deployment-wide view used by the worker's Prometheus
// gauges. It never carries tenant payloads, only counts and an age.
type PlatformSnapshot struct {
	OutboxBacklog                  int64   `json:"outboxBacklog"`
	InboxExpiredLeases             int64   `json:"inboxExpiredLeases"`
	ProjectionPendingRuns          int64   `json:"projectionPendingRuns"`
	ProjectionOldestPendingSeconds float64 `json:"projectionOldestPendingSeconds"`
}

func (s *Service) Snapshot(ctx context.Context, tenantID string, since time.Time, outboxMaxAttempts int) (Snapshot, error) {
	return s.store.Snapshot(ctx, tenantID, since, outboxMaxAttempts)
}

func (s *Service) PlatformSnapshot(ctx context.Context, now time.Time, outboxMaxAttempts int) (PlatformSnapshot, error) {
	return s.store.PlatformSnapshot(ctx, now, outboxMaxAttempts)
}
