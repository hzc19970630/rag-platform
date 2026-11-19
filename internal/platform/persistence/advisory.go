package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"gorm.io/gorm"
)

// SessionLock is a held PostgreSQL advisory lock with the dedicated connection
// that owns it. Healthy reports whether that session is still usable, so a
// long-lived leader can step down after a database restart instead of running
// without the lock.
type SessionLock struct {
	connection *sql.Conn
	key        int64

	mu       sync.Mutex
	released bool
}

// TrySessionLock takes a non-blocking session-level advisory lock on a
// dedicated connection. Holding it on a pooled connection would be unsafe:
// PostgreSQL scopes advisory locks to a session, and the pool could hand later
// statements to a different connection.
func TrySessionLock(ctx context.Context, database *gorm.DB, key int64) (*SessionLock, bool, error) {
	sqlDB, err := database.DB()
	if err != nil {
		return nil, false, fmt.Errorf("database handle: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire lock connection: %w", err)
	}
	var locked bool
	if err := connection.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&locked); err != nil {
		_ = connection.Close()
		return nil, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !locked {
		_ = connection.Close()
		return nil, false, nil
	}
	return &SessionLock{connection: connection, key: key}, true, nil
}

// Healthy pings the lock session. A false result means PostgreSQL already
// released the lock with the broken session, so the caller must re-acquire.
func (l *SessionLock) Healthy(ctx context.Context) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return false
	}
	return l.connection.PingContext(ctx) == nil
}

// DefaultXactLockTimeout bounds how long a resource lock waits before the
// caller fails with a conflict instead of queueing indefinitely.
const DefaultXactLockTimeout = 3 * time.Second

// LockResourceXact takes a transaction-scoped PostgreSQL advisory lock on key.
// SQLite (unit tests) has no advisory locks and is single-writer, so the call
// is a no-op there. A lock timeout maps to apperror.ErrConflict, which the HTTP
// layer renders as 409 resource_busy.
func LockResourceXact(tx *gorm.DB, key string, timeout time.Duration) error {
	if tx == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultXactLockTimeout
	}
	// SET does not accept bind parameters; set_config with local=true is the
	// parameterized equivalent scoped to this transaction.
	if err := tx.Exec("SELECT set_config('lock_timeout', ?, true)", fmt.Sprintf("%dms", timeout.Milliseconds())).Error; err != nil {
		return fmt.Errorf("set lock timeout: %w", err)
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
		var state interface{ SQLState() string }
		if errors.As(err, &state) && state.SQLState() == "55P03" {
			return fmt.Errorf("resource_busy: %w", apperror.ErrConflict)
		}
		return fmt.Errorf("acquire resource lock: %w", err)
	}
	return nil
}

// LockKnowledgeBaseXact serializes knowledge-base tree mutations and deletes.
// It is deliberately not taken by uploads: they already serialize on the
// tenant quota lock, and a KB lock there would throttle ingestion.
func LockKnowledgeBaseXact(tx *gorm.DB, tenantID, knowledgeBaseID string) error {
	return LockResourceXact(tx, "kb:"+tenantID+":"+knowledgeBaseID, DefaultXactLockTimeout)
}

// LockTenantXact uses the same key as the upload quota lock, so a knowledge
// base delete cannot interleave with an in-flight upload in that tenant.
func LockTenantXact(tx *gorm.DB, tenantID string) error {
	return LockResourceXact(tx, tenantID, DefaultXactLockTimeout)
}

// Release is idempotent and closes the session, so a process exit also
// releases the lock.
func (l *SessionLock) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Closing the session releases the lock too, so an unlock error cannot
	// leak it past this process.
	_, _ = l.connection.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", l.key)
	_ = l.connection.Close()
}
