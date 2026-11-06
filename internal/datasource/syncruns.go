package datasource

import (
	"context"
	"errors"
	"fmt"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"strings"
	"time"
)

// SyncRunManager owns one slice of the datasource use cases and shares the
// service dependencies through the embedded service.
type SyncRunManager struct{ *Service }

func (s *SyncRunManager) TriggerSync(ctx context.Context, principal authz.Principal, connectorID string) (RunView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return RunView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return RunView{}, err
	}
	if !connector.Enabled {
		return RunView{}, apperror.Invalidf("connector must be enabled before syncing")
	}
	run, err := s.createRun(ctx, connector, ModeManual, "", "{}", principal.SubjectID)
	if err != nil {
		return RunView{}, err
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_triggered:"+run.ID); err != nil {
		return RunView{}, err
	}
	return runView(run), nil
}

func (s *SyncRunManager) ListRuns(ctx context.Context, principal authz.Principal, connectorID string, limit int) ([]RunView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var runs []models.SyncRun
	if err := s.db.WithContext(ctx).Where("connector_id = ?", connector.ID).Order("created_at DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list sync runs: %w", err)
	}
	views := make([]RunView, 0, len(runs))
	for _, run := range runs {
		views = append(views, runView(run))
	}
	return views, nil
}

func (s *SyncRunManager) GetRun(ctx context.Context, principal authz.Principal, connectorID, runID string) (RunView, error) {
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "read")
	if err != nil {
		return RunView{}, err
	}
	var run models.SyncRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND connector_id = ? AND id = ?", principal.TenantID, connector.ID, runID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunView{}, apperror.ErrNotFound
		}
		return RunView{}, fmt.Errorf("load sync run: %w", err)
	}
	return runView(run), nil
}

func (s *SyncRunManager) RetryRun(ctx context.Context, principal authz.Principal, connectorID, runID string) (RunView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return RunView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return RunView{}, err
	}
	var source models.SyncRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND connector_id = ? AND id = ?", principal.TenantID, connector.ID, runID).First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunView{}, apperror.ErrNotFound
		}
		return RunView{}, fmt.Errorf("load sync run: %w", err)
	}
	switch source.Status {
	case StatusFailed, StatusPartial, StatusCancelled:
	default:
		return RunView{}, fmt.Errorf("%w: only a failed, partial or cancelled run can be retried", apperror.ErrConflict)
	}
	cursor := source.Cursor
	if strings.TrimSpace(cursor) == "" {
		cursor = "{}"
	}
	run, err := s.createRun(ctx, connector, ModeResume, source.ID, cursor, principal.SubjectID)
	if err != nil {
		return RunView{}, err
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_retried:"+source.ID+"->"+run.ID); err != nil {
		return RunView{}, err
	}
	return runView(run), nil
}

func (s *SyncRunManager) CancelRun(ctx context.Context, principal authz.Principal, connectorID, runID string) (RunView, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return RunView{}, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return RunView{}, err
	}
	var run models.SyncRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND connector_id = ? AND id = ?", principal.TenantID, connector.ID, runID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunView{}, apperror.ErrNotFound
		}
		return RunView{}, fmt.Errorf("load sync run: %w", err)
	}
	now := time.Now()
	switch run.Status {
	case StatusPending:
		if err := s.db.WithContext(ctx).Model(&models.SyncRun{}).Where("id = ? AND status = ?", run.ID, StatusPending).
			Updates(map[string]any{"status": StatusCancelled, "cancel_requested": true, "finished_at": &now, "updated_at": now}).Error; err != nil {
			return RunView{}, fmt.Errorf("cancel sync run: %w", err)
		}
	case StatusRunning:
		if err := s.db.WithContext(ctx).Model(&models.SyncRun{}).Where("id = ? AND status = ?", run.ID, StatusRunning).
			Updates(map[string]any{"cancel_requested": true, "updated_at": now}).Error; err != nil {
			return RunView{}, fmt.Errorf("cancel sync run: %w", err)
		}
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_cancelled:"+run.ID); err != nil {
		return RunView{}, err
	}
	run, err = s.reloadRun(ctx, run.ID)
	if err != nil {
		return RunView{}, err
	}
	return runView(run), nil
}

func (s *SyncRunManager) CancelConnector(ctx context.Context, principal authz.Principal, connectorID string) (int, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return 0, err
	}
	connector, err := s.authorizedConnector(ctx, principal, connectorID, "write")
	if err != nil {
		return 0, err
	}
	count, err := s.cancelActiveRuns(ctx, principal.TenantID, []string{connector.ID})
	if err != nil {
		return 0, err
	}
	if err := s.audit(ctx, principal, "write", connector.ID, "sync_cancelled_connector"); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *SyncRunManager) CancelKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (int, error) {
	if err := s.authz.RequireTenantOperator(ctx, principal); err != nil {
		return 0, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return 0, err
	}
	var ids []string
	if err := s.db.WithContext(ctx).Model(&models.Connector{}).Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, knowledgeBaseID).Pluck("id", &ids).Error; err != nil {
		return 0, fmt.Errorf("list knowledge base connectors: %w", err)
	}
	count, err := s.cancelActiveRuns(ctx, principal.TenantID, ids)
	if err != nil {
		return 0, err
	}
	if err := s.audit(ctx, principal, "write", knowledgeBaseID, "sync_cancelled_knowledge_base"); err != nil {
		return 0, err
	}
	return count, nil
}
