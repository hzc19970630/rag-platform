package datasource

import (
	"context"
	"errors"
	"fmt"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"time"
)

// MaintenanceManager owns one slice of the datasource use cases and shares the
// service dependencies through the embedded service.
type MaintenanceManager struct{ *Service }

// ScheduleDue turns due connectors into durable PENDING runs. It is called from
// the worker maintenance loop, not from the API request path.
func (s *MaintenanceManager) ScheduleDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now()
	var connectors []models.Connector
	if err := tenantdb.Unscoped(s.db.WithContext(ctx), "connector scheduler: list due connectors across tenants").
		Where("enabled = TRUE AND next_sync_at IS NOT NULL AND next_sync_at <= ?", now).
		Order("next_sync_at").Limit(limit).Find(&connectors).Error; err != nil {
		return 0, fmt.Errorf("list due connectors: %w", err)
	}
	triggered := 0
	var firstErr error
	for _, connector := range connectors {
		_, err := s.createRun(ctx, connector, ModeScheduled, "", "{}", "scheduler")
		if err != nil && !errors.Is(err, apperror.ErrConflict) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err == nil {
			triggered++
		}
		var next *time.Time
		if connector.SyncIntervalSeconds > 0 {
			value := now.Add(time.Duration(connector.SyncIntervalSeconds) * time.Second)
			next = &value
		}
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), connector.TenantID).Model(&models.Connector{}).Where("id = ?", connector.ID).
			Updates(map[string]any{"next_sync_at": next, "updated_at": now}).Error; err != nil && firstErr == nil {
			firstErr = fmt.Errorf("reschedule connector %s: %w", connector.ID, err)
		}
	}
	return triggered, firstErr
}

// ExpireStaleRuns recovers work that Redis lost after the run row was written.
// Without it the one-active-run index would block that connector forever.
func (s *MaintenanceManager) ExpireStaleRuns(ctx context.Context, now time.Time) (int, error) {
	base := tenantdb.Unscoped(s.db.WithContext(ctx), "connector maintenance: expire stale sync runs across tenants")
	result := base.Model(&models.SyncRun{}).
		Where("status = ? AND lease_until IS NOT NULL AND lease_until < ?", StatusRunning, now.Add(-5*time.Minute)).
		Updates(map[string]any{"status": StatusFailed, "error_summary": "worker lease expired", "finished_at": &now, "updated_at": now})
	if result.Error != nil {
		return 0, fmt.Errorf("expire stale sync runs: %w", result.Error)
	}
	expired := int(result.RowsAffected)
	stalePending := tenantdb.Unscoped(s.db.WithContext(ctx), "connector maintenance: expire stale sync runs across tenants").Model(&models.SyncRun{}).
		Where("status = ? AND created_at < ?", StatusPending, now.Add(-time.Hour)).
		Updates(map[string]any{"status": StatusFailed, "error_summary": "sync dispatch timed out", "finished_at": &now, "updated_at": now})
	if stalePending.Error != nil {
		return expired, fmt.Errorf("expire pending sync runs: %w", stalePending.Error)
	}
	return expired + int(stalePending.RowsAffected), nil
}

// FailRun is the terminal hook for an exhausted Asynq retry budget. It only
// touches active runs, so a late failure cannot overwrite a completed result.
func (s *MaintenanceManager) FailRun(ctx context.Context, task SyncTask, cause error) error {
	if task.SyncRunID == "" {
		return nil
	}
	message := "sync worker exhausted retries"
	if cause != nil {
		message = truncate("sync worker exhausted retries: "+cause.Error(), 2000)
	}
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("id = ? AND tenant_id = ? AND status IN ?", task.SyncRunID, task.TenantID, []string{StatusPending, StatusRunning}).
		Updates(map[string]any{"status": StatusFailed, "error_summary": message, "finished_at": &now, "updated_at": now}).Error
}
