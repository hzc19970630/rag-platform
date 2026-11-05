package ingestion

import (
	"context"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/pipeline"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// lifecycle_retention.go owns history retention: finished ingestion pipeline
// runs and resolved dead letters. Documents, versions, chunks and evidence
// packages are never touched here; their lifecycle belongs to retention of the
// content itself (DOCUMENT_RETENTION_DAYS).

// terminalPipelineStatuses are the statuses a pipeline run never leaves by
// itself. Only these rows are historical; anything in flight is kept.
var terminalPipelineStatuses = []string{StatusEvidenceReady, StatusFailed, StatusCancelled}

// PurgeTaskHistory deletes finished pipeline runs and resolved dead letters
// older than before, at most limit rows of each kind per call so a large
// backlog drains in batches instead of one long transaction.
func (s *Service) PurgeTaskHistory(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 200
	}
	type expiredPipeline struct {
		ID                string
		DocumentVersionID string
	}
	var pipelines []expiredPipeline
	if err := s.db.WithContext(ctx).
		Model(&models.PipelineRun{}).
		Select("id", "document_version_id").
		Where("pipeline_type = ? AND status IN ? AND updated_at < ?", PipelineTypeEvidence, terminalPipelineStatuses, before).
		Order("updated_at, id").
		Limit(limit).
		Find(&pipelines).Error; err != nil {
		return 0, fmt.Errorf("list expired pipeline runs: %w", err)
	}
	var deadLetterIDs []uint64
	if err := s.db.WithContext(ctx).
		Model(&models.TaskDeadLetter{}).
		Where("status IN ? AND resolved_at IS NOT NULL AND resolved_at < ?", []string{pipeline.DeadLetterResolved, pipeline.DeadLetterDiscarded}, before).
		Order("resolved_at, id").
		Limit(limit).
		Pluck("id", &deadLetterIDs).Error; err != nil {
		return 0, fmt.Errorf("list expired dead letters: %w", err)
	}
	if len(pipelines) == 0 && len(deadLetterIDs) == 0 {
		return 0, nil
	}
	pipelineIDs := make([]string, 0, len(pipelines))
	versionIDs := make([]string, 0, len(pipelines))
	seenVersions := make(map[string]bool, len(pipelines))
	for _, expired := range pipelines {
		pipelineIDs = append(pipelineIDs, expired.ID)
		if expired.DocumentVersionID == "" || seenVersions[expired.DocumentVersionID] {
			continue
		}
		seenVersions[expired.DocumentVersionID] = true
		versionIDs = append(versionIDs, expired.DocumentVersionID)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(pipelineIDs) > 0 {
			if err := tx.Where("pipeline_run_id IN ?", pipelineIDs).Delete(&models.StageRun{}).Error; err != nil {
				return err
			}
			// Outbox events are published with either the pipeline run or the
			// document version as their aggregate.
			aggregateIDs := append(append([]string{}, pipelineIDs...), versionIDs...)
			if err := tx.Where("aggregate_id IN ?", aggregateIDs).Delete(&models.OutboxEvent{}).Error; err != nil {
				return err
			}
			for _, pipelineID := range pipelineIDs {
				if err := tx.Where("idempotency_key LIKE ?", pipelineID+":%").Delete(&models.TaskInbox{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("id IN ?", pipelineIDs).Delete(&models.PipelineRun{}).Error; err != nil {
				return err
			}
		}
		if len(deadLetterIDs) > 0 {
			if err := tx.Where("id IN ?", deadLetterIDs).Delete(&models.TaskDeadLetter{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("purge task history: %w", err)
	}
	return int64(len(pipelines) + len(deadLetterIDs)), nil
}
