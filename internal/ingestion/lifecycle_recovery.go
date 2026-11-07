package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/authz"
	pipelineruntime "github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// lifecycle_recovery.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

func (s *Service) readyPackages(ctx context.Context, tenantID string, versions []models.DocumentVersion) (map[string]bool, error) {
	ready := map[string]bool{}
	ids := make([]string, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	if len(ids) == 0 {
		return ready, nil
	}
	// A superseded version's package is STALE but intact, so it stays
	// restorable: rollback reactivates the existing chunks instead of
	// re-embedding them.
	var packages []models.EvidencePackage
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("document_version_id IN ? AND status IN ?", ids, []string{StatusEvidenceReady, StatusStale}).Find(&packages).Error; err != nil {
		return nil, fmt.Errorf("load evidence packages: %w", err)
	}
	for _, pkg := range packages {
		ready[pkg.DocumentVersionID] = true
	}
	return ready, nil
}

func (s *Service) versionReady(ctx context.Context, tenantID string, version models.DocumentVersion) (bool, error) {
	if version.Status == StatusEvidenceReady {
		return true, nil
	}
	var count int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.EvidencePackage{}).Where("document_version_id = ? AND status IN ?", version.ID, []string{StatusEvidenceReady, StatusStale}).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check evidence package: %w", err)
	}
	return count > 0, nil
}

// restartTargetStage is the first stage that never succeeded. A missing stage
// means processing stopped before it was created. An empty result means the
// pipeline already finished every stage.
func restartTargetStage(stages []models.StageRun) string {
	byStage := make(map[string]models.StageRun, len(stages))
	for _, stage := range stages {
		byStage[stage.Stage] = stage
	}
	for _, stage := range stageOrder {
		if current, ok := byStage[stage]; !ok || current.Status != StageSucceeded {
			return stage
		}
	}
	return ""
}

// restartDeletionCancelledPipeline resumes a pipeline that a document deletion
// cancelled while its stages were still running. A reappearing connector item
// reuses the same document, version and stage keys, so the pipeline must continue
// from the first stage that never succeeded instead of being returned as a dead
// record. A version whose evidence survived the delete is already searchable, so
// only its stale cancelled records are settled.

// restartDeletionCancelledPipeline resumes a pipeline that a document deletion
// cancelled while its stages were still running. A reappearing connector item
// reuses the same document, version and stage keys, so the pipeline must continue
// from the first stage that never succeeded instead of being returned as a dead
// record. A version whose evidence survived the delete is already searchable, so
// only its stale cancelled records are settled.
func (s *Service) restartDeletionCancelledPipeline(ctx context.Context, principal authz.Principal, pipelineID string) (bool, error) {
	var pipeline models.PipelineRun
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&pipeline, "id = ?", pipelineID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load pipeline for restart: %w", err)
	}
	if pipeline.Status != StatusCancelled || pipeline.ErrorCode != "deleted" {
		return false, nil
	}
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&document, "id = ?", pipeline.DocumentID).Error; err != nil {
		return false, fmt.Errorf("load document for pipeline restart: %w", err)
	}
	if document.DeletedAt != nil || document.LatestVersionID != pipeline.DocumentVersionID {
		return false, nil
	}
	var version models.DocumentVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&version, "id = ?", pipeline.DocumentVersionID).Error; err != nil {
		return false, fmt.Errorf("load version for pipeline restart: %w", err)
	}
	ready, err := s.versionReady(ctx, principal.TenantID, version)
	if err != nil {
		return false, err
	}
	var stages []models.StageRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", pipeline.ID).Find(&stages).Error; err != nil {
		return false, fmt.Errorf("load stages for pipeline restart: %w", err)
	}
	byStage := make(map[string]models.StageRun, len(stages))
	for _, stage := range stages {
		byStage[stage.Stage] = stage
	}
	target := restartTargetStage(stages)
	if target == "" {
		return false, nil
	}
	now := time.Now()
	// The evidence package survived the delete, so the document is already
	// searchable. The pipeline can still show CANCELLED when the delete landed
	// between the index transaction and its stage completion; settle the records
	// instead of re-running work that already happened.
	if ready {
		err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
			for _, stage := range stageOrder {
				current, ok := byStage[stage]
				if !ok {
					continue
				}
				if current.Status != StageSucceeded {
					if err := tx.Model(&models.StageRun{}).Where("id = ?", current.ID).Updates(map[string]any{
						"status": StageSucceeded, "error_code": "", "error_message": "",
						"completed_at": &now, "updated_at": now,
					}).Error; err != nil {
						return err
					}
				}
				if err := pipelineruntime.SettleInbox(tx, current.IdempotencyKey, pipelineruntime.InboxSucceeded, now); err != nil {
					return err
				}
			}
			if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.PipelineRun{}).Where("id = ?", pipeline.ID).Updates(statusFields(StatusEvidenceReady, map[string]any{"updated_at": now})).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(statusFields(StatusEvidenceReady, map[string]any{"updated_at": now})).Error; err != nil {
				return err
			}
			return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{"status": StatusEvidenceReady, "updated_at": now}).Error
		})
		if err != nil {
			return false, fmt.Errorf("finish restored pipeline: %w", err)
		}
		return true, nil
	}
	predecessor := StatusBeforeStage(target)
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		rewinding := false
		for _, stage := range stageOrder {
			if stage == target {
				rewinding = true
			}
			if !rewinding {
				continue
			}
			current, ok := byStage[stage]
			if !ok {
				continue
			}
			if err := tx.Model(&models.StageRun{}).Where("id = ?", current.ID).Updates(map[string]any{
				"status": StagePending, "attempts": 0, "error_code": "", "error_message": "",
				"started_at": nil, "completed_at": nil, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			// A task that ran while the document was deleted returned early and
			// recorded the inbox row as SUCCEEDED. The stage itself never ran, so
			// the row must stop blocking the restarted task.
			if err := pipelineruntime.RequeueInbox(tx, current.IdempotencyKey, now); err != nil {
				return err
			}
		}
		pipelineFields := statusFields(predecessor, map[string]any{"updated_at": now})
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.PipelineRun{}).Where("id = ?", pipeline.ID).Updates(pipelineFields).Error; err != nil {
			return err
		}
		versionFields := statusFields(predecessor, map[string]any{"updated_at": now})
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(versionFields).Error; err != nil {
			return err
		}
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{"status": StatusStored, "updated_at": now}).Error; err != nil {
			return err
		}
		targetStage, ok := byStage[target]
		if !ok {
			targetStage = models.StageRun{PipelineRunID: pipeline.ID, Stage: target, Status: StagePending, IdempotencyKey: pipeline.ID + ":" + target}
			if err := tx.Create(&targetStage).Error; err != nil {
				return err
			}
		}
		task := s.taskFor(principal, document, version, pipeline, targetStage, target)
		payload, err := json.Marshal(task)
		if err != nil {
			return fmt.Errorf("marshal restarted task: %w", err)
		}
		return pipelineruntime.ReopenEvent(tx, models.OutboxEvent{
			TenantID: pipeline.TenantID, AggregateType: "pipeline_run", AggregateID: pipeline.ID,
			EventType: "document." + target, IdempotencyKey: targetStage.IdempotencyKey, Payload: string(payload),
		})
	})
	if err != nil {
		return false, fmt.Errorf("restart cancelled pipeline: %w", err)
	}
	return true, nil
}

func cancelOtherPipelines(tx *gorm.DB, tenantID, documentID, keepVersionID string) error {
	now := time.Now()
	return tenantdb.MustScoped(tx, tenantID).Model(&models.PipelineRun{}).Where("document_id = ? AND document_version_id <> ? AND status NOT IN ?", documentID, keepVersionID, []string{StatusEvidenceReady, StatusCancelled, StatusFailed}).
		Updates(map[string]any{"status": StatusCancelled, "error_code": "superseded", "error_message": "superseded by another version", "updated_at": now}).Error
}

// restartTargetStage is the first stage that never succeeded. A missing stage
// means processing stopped before it was created. An empty result means the
// pipeline already finished every stage.

func cancelPendingOutbox(tx *gorm.DB, tenantID string, versionIDs []string) error {
	now := time.Now()
	return tenantdb.MustScoped(tx, tenantID).Model(&models.OutboxEvent{}).Where("aggregate_id IN ? AND published_at IS NULL", versionIDs).Updates(map[string]any{"published_at": &now, "last_error": "cancelled"}).Error
}
