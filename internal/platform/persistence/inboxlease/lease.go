// Package inboxlease gives task_inbox rows an ownership lease, so two workers
// that receive the same delivery cannot execute the same stage concurrently.
// The lease is a conditional row update; no transaction or row lock is needed.
package inboxlease

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	// DefaultTTL outlives one stage attempt by a wide margin: the worker renews
	// it every third of the TTL while the stage runs.
	DefaultTTL = 2 * time.Minute

	StatusProcessing = "PROCESSING"
	StatusSucceeded  = "SUCCEEDED"
)

// Owner lazily assigns one immutable identity to a worker. A worker built as a
// zero-value struct (tests, tooling) still gets a unique owner on first use.
type Owner struct {
	once  sync.Once
	value string
}

func (o *Owner) Get() string {
	o.once.Do(func() {
		o.value = NewOwner()
	})
	return o.value
}

func NewOwner() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "worker"
	}
	return fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano())
}

type Lease struct {
	Owner string
	TTL   time.Duration
}

func New(owner string, ttl time.Duration) Lease {
	return Lease{Owner: owner, TTL: ttl}
}

func (l Lease) ttl() time.Duration {
	if l.TTL <= 0 {
		return DefaultTTL
	}
	return l.TTL
}

func (l Lease) HeartbeatInterval() time.Duration {
	interval := l.ttl() / 3
	if interval <= 0 {
		interval = time.Second
	}
	return interval
}

// Claim takes the inbox row for lease.Owner. It succeeds only when the row is
// idle (never started, failed, reopened) or when its previous lease expired.
// SUCCEEDED rows are never reclaimed, and a live PROCESSING lease is left
// untouched. A missing row is created already leased.
func Claim(ctx context.Context, db *gorm.DB, key, taskType string, stageRunID uint64, lease Lease) (bool, error) {
	if lease.Owner == "" {
		return false, errors.New("inbox lease owner is required")
	}
	now := time.Now()
	until := now.Add(lease.ttl())
	tenantID := resolveTenant(ctx, db, key)
	claimed, err := take(ctx, db, key, tenantID, lease, now, until)
	if err != nil || claimed {
		return claimed, err
	}

	var count int64
	if err := inboxDB(db).WithContext(ctx).Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}

	row := models.TaskInbox{
		IdempotencyKey: key, TaskType: taskType, Status: StatusProcessing, StageRunID: stageRunID,
		TenantID: tenantID, LeaseOwner: lease.Owner, LeaseUntil: &until, CreatedAt: now, UpdatedAt: now,
	}
	if createErr := inboxDB(db).WithContext(ctx).Create(&row).Error; createErr != nil {
		// Another delivery inserted first. Re-running the conditional take either
		// wins against a live row that is already gone or reports the row exists.
		claimed, takeErr := take(ctx, db, key, tenantID, lease, now, until)
		if takeErr != nil {
			return false, fmt.Errorf("claim inbox after insert race: %w", takeErr)
		}
		if claimed {
			return true, nil
		}
		if countErr := inboxDB(db).WithContext(ctx).Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Count(&count).Error; countErr != nil {
			return false, countErr
		}
		if count == 0 {
			return false, fmt.Errorf("create inbox row: %w", createErr)
		}
		return false, nil
	}
	return true, nil
}

// inboxDB marks the deliberate bypass: task_inbox is a child table keyed by
// idempotency key, so its rows are reached through the owning pipeline or
// projection run rather than a tenant column.
func inboxDB(db *gorm.DB) *gorm.DB {
	return tenantdb.Unscoped(db, "inbox lease: task_inbox is scoped by idempotency key")
}

// resolveTenant maps an idempotency key back to the tenant of its owning run.
// Claim runs outside any request scope, so the value is copied onto the inbox
// row instead of being read from app.tenant_id. An unresolvable key is left
// NULL rather than failing the claim: attribution is an audit concern and RLS
// enforcement reports these rows separately.
func resolveTenant(ctx context.Context, db *gorm.DB, key string) *string {
	runID, _, ok := strings.Cut(key, ":")
	if !ok || runID == "" {
		return nil
	}
	// pipeline_runs and projection_runs are tenant tables, but resolution is
	// the deliberate cross-tenant lookup that precedes knowing the tenant.
	owner := tenantdb.Unscoped(db, "inbox lease: resolve owning run tenant")
	for _, table := range []string{"pipeline_runs", "projection_runs"} {
		if !owner.Migrator().HasTable(table) {
			continue
		}
		var row struct{ TenantID string }
		if err := owner.WithContext(ctx).Table(table).Select("tenant_id").Where("id = ?", runID).Limit(1).Scan(&row).Error; err != nil {
			slog.Warn("inbox_tenant_resolve_failed", "idempotency_key", key, "table", table, "error", err)
			return nil
		}
		if row.TenantID != "" {
			tenantID := row.TenantID
			return &tenantID
		}
	}
	return nil
}

func take(ctx context.Context, db *gorm.DB, key string, tenantID *string, lease Lease, now, until time.Time) (bool, error) {
	values := map[string]any{
		"status": StatusProcessing, "lease_owner": lease.Owner, "lease_until": until,
		"last_error": "", "updated_at": now,
	}
	if tenantID != nil {
		values["tenant_id"] = *tenantID
	}
	result := inboxDB(db).WithContext(ctx).Model(&models.TaskInbox{}).
		Where("idempotency_key = ? AND status <> ?", key, StatusSucceeded).
		Where("(lease_until IS NULL OR lease_until <= ?)", now).
		Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// Renew extends a lease only while the caller still owns the row. A false
// result means the lease expired and another worker took over.
func Renew(ctx context.Context, db *gorm.DB, key string, lease Lease) (bool, error) {
	if lease.Owner == "" {
		return false, errors.New("inbox lease owner is required")
	}
	now := time.Now()
	result := inboxDB(db).WithContext(ctx).Model(&models.TaskInbox{}).
		Where("idempotency_key = ? AND status = ? AND lease_owner = ?", key, StatusProcessing, lease.Owner).
		Updates(map[string]any{"lease_until": now.Add(lease.ttl()), "updated_at": now})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// Release clears the lease so a retry can claim the row immediately. It never
// touches a lease owned by another worker.
func Release(ctx context.Context, db *gorm.DB, key, owner string) error {
	if owner == "" {
		return nil
	}
	return inboxDB(db).WithContext(ctx).Model(&models.TaskInbox{}).
		Where("idempotency_key = ? AND lease_owner = ?", key, owner).
		Updates(map[string]any{"lease_owner": "", "lease_until": nil}).Error
}

// Keep renews lease in the background until stop is called or ctx ends. When
// the lease can no longer be renewed it invokes onLost exactly once and exits;
// the caller should stop writing results for the task at that point.
func Keep(ctx context.Context, db *gorm.DB, key string, lease Lease, onLost func()) func() {
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() { close(done) })
	}
	go func() {
		ticker := time.NewTicker(lease.HeartbeatInterval())
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				renewed, err := Renew(renewCtx, db, key, lease)
				cancel()
				if err != nil {
					slog.Warn("inbox_lease_renew_failed", "idempotency_key", key, "error", err)
					continue
				}
				if !renewed {
					if onLost != nil {
						onLost()
					}
					return
				}
			}
		}
	}()
	return stop
}
