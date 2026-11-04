package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

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
