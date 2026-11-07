package knowledgeprojection

import (
	"context"
	"errors"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/relationstore"
)

// graph_runs.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// ListRelationRuns lists relation projection runs for the graph run panel.
func (s *Service) ListRelationRuns(ctx context.Context, principal authz.Principal, knowledgeBaseID string, limit int) ([]models.ProjectionRun, error) {
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var runs []models.ProjectionRun
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeRelation).
		Order("created_at DESC, id DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// GetGraphRun returns the relation run detail with its current snapshot.
func (s *Service) GetGraphRun(ctx context.Context, principal authz.Principal, runID string) (GraphRunDetail, error) {
	var run models.ProjectionRun
	if err := s.db.WithContext(ctx).First(&run, "id = ? AND tenant_id = ? AND projection_type = ?", runID, principal.TenantID, ProjectionTypeRelation).Error; err != nil {
		return GraphRunDetail{}, apperror.ErrNotFound
	}
	if _, err := s.authorize(ctx, principal, run.KnowledgeBaseID, "read"); err != nil {
		return GraphRunDetail{}, err
	}
	var stages []models.ProjectionStageRun
	if err := s.db.WithContext(ctx).Where("projection_run_id = ?", run.ID).Order("id").Find(&stages).Error; err != nil {
		return GraphRunDetail{}, err
	}
	detail := GraphRunDetail{Run: run, Stages: stages}
	if s.store != nil {
		snapshot, err := s.store.SnapshotByRun(ctx, run.ID)
		if err == nil {
			model := snapshotToModel(*snapshot)
			detail.Snapshot = &model
		} else if !errors.Is(err, relationstore.ErrNotFound) {
			return GraphRunDetail{}, err
		}
	}
	return detail, nil
}
