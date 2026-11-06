package ops

import (
	"context"
	"time"
)

// Store is the narrow read port the operator snapshot needs. It is implemented
// by PostgreSQL today; a second storage shape can be added without changing
// the service or its HTTP transport.
type Store interface {
	Snapshot(ctx context.Context, tenantID string, since time.Time, outboxMaxAttempts int) (Snapshot, error)
}
