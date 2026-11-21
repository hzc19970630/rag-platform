package knowledgeprojection

import (
	"context"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

// SchedulePendingChanges turns accumulated evidence changes into one batch run
// per projection after the quiet window. A run that finished for an older epoch
// is left SUPERSEDED by the completion path; this sweep starts the catch-up.
//
// The quiet window is measured from the last change; MaxWait forces progress
// when a knowledge base keeps receiving writes. Manual rebuild endpoints bypass
// this sweep and create their run immediately.
func (s *Service) SchedulePendingChanges(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	var states []models.KnowledgeProjectionState
	if err := tenantdb.Unscoped(s.db.WithContext(ctx), "projection batch scheduler: pending epochs across tenants").
		Where("target_epoch > wiki_applied_epoch OR target_epoch > relation_applied_epoch").
		Order("updated_at, tenant_id, knowledge_base_id").Limit(limit).
		Find(&states).Error; err != nil {
		return 0, fmt.Errorf("list pending projection states: %w", err)
	}
	now := time.Now()
	started := 0
	for _, state := range states {
		if state.LastChangeAt != nil {
			elapsed := now.Sub(*state.LastChangeAt)
			if elapsed < s.batchQuiet && elapsed < s.batchMaxWait {
				continue
			}
		}
		var knowledgeBase models.KnowledgeBase
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), state.TenantID).
			Where("id = ?", state.KnowledgeBaseID).First(&knowledgeBase).Error; err != nil {
			continue
		}
		strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
		if err != nil {
			continue
		}
		packages, err := s.loadCurrentPackages(ctx, state.TenantID, state.KnowledgeBaseID)
		if err != nil || len(packages) == 0 {
			continue
		}
		if strategy.WikiEnabled && state.WikiAppliedEpoch < state.TargetEpoch {
			created, err := s.scheduleProjection(ctx, knowledgeBase, packages, ProjectionTypeWiki, s.generator.PromptVersion(), s.generator.Model(), true)
			if err != nil {
				return started, err
			}
			if created {
				started++
				s.metrics.Inc("projection_batch_scheduled_total", metrics.Labels("projection_type", ProjectionTypeWiki))
			}
		}
		if strategy.GraphEnabled && s.relation != nil && state.RelationAppliedEpoch < state.TargetEpoch {
			created, err := s.scheduleProjection(ctx, knowledgeBase, packages, ProjectionTypeRelation, s.relation.PromptVersion(), s.relation.Model(), true)
			if err != nil {
				return started, err
			}
			if created {
				started++
				s.metrics.Inc("projection_batch_scheduled_total", metrics.Labels("projection_type", ProjectionTypeRelation))
			}
		}
	}
	return started, nil
}
