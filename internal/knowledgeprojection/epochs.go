package knowledgeprojection

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// projectionTargetEpoch reads the current target epoch for a knowledge base.
func (s *Service) projectionTargetEpoch(ctx context.Context, tenantID, knowledgeBaseID string) (int64, error) {
	var state models.KnowledgeProjectionState
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).
		First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return state.TargetEpoch, nil
}

// supersedeStaleRun marks a run that was built for an older epoch as
// SUPERSEDED. Its pages/snapshot must not be published; the batch scheduler
// starts the catch-up run.
func (s *Service) supersedeStaleRun(ctx context.Context, run *models.ProjectionRun) (bool, error) {
	target, err := s.projectionTargetEpoch(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return false, err
	}
	if target <= run.TargetEpoch {
		return false, nil
	}
	return true, s.markRunSuperseded(ctx, run, fmt.Sprintf("newer evidence epoch %d superseded this run", target))
}

func (s *Service) markRunSuperseded(ctx context.Context, run *models.ProjectionRun, message string) error {
	now := time.Now()
	if s.metrics != nil {
		s.metrics.Inc("projection_run_superseded_total", metrics.Labels("projection_type", run.ProjectionType))
	}
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": ProjectionSuperseded, "error_code": "epoch_superseded",
		"error_message": message, "completed_at": &now, "updated_at": now,
	}).Error
}

// markProjectionApplied advances one projection's applied epoch, clears the
// invalidation gate when the target is covered, and marks the change log
// applied. It is called only after the derived content is actually visible.
func (s *Service) markProjectionApplied(ctx context.Context, tenantID, knowledgeBaseID, projectionType string, epoch int64) error {
	if epoch <= 0 {
		return nil
	}
	column := "wiki_applied_epoch"
	if projectionType == ProjectionTypeRelation {
		column = "relation_applied_epoch"
	}
	now := time.Now().UTC()
	return tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		update := fmt.Sprintf(`UPDATE knowledge_projection_states
			SET %s = CASE WHEN %s < ? THEN ? ELSE %s END,
			    invalidated_at = CASE WHEN target_epoch <= ? THEN NULL ELSE invalidated_at END,
			    updated_at = ?
			WHERE tenant_id = ? AND knowledge_base_id = ?`, column, column, column)
		if err := tx.Exec(update, epoch, epoch, epoch, now, tenantID, knowledgeBaseID).Error; err != nil {
			return fmt.Errorf("advance %s applied epoch: %w", projectionType, err)
		}
		return tenantdb.MustScoped(tx, tenantID).Model(&models.ProjectionChange{}).
			Where("knowledge_base_id = ? AND applied_at IS NULL AND epoch <= ?", knowledgeBaseID, epoch).
			Update("applied_at", now).Error
	})
}
