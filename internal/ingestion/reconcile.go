package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
)

// ReconcileReport is the operator-facing summary of one consistency pass.
type ReconcileReport struct {
	DocumentsRepaired   int64 `json:"documentsRepaired"`
	VersionsRepaired    int64 `json:"versionsRepaired"`
	StagesRequeued      int   `json:"stagesRequeued"`
	PipelinesCancelled  int   `json:"pipelinesCancelled"`
	InboxesExpired      int64 `json:"inboxesExpired"`
	DeadLettersResolved int64 `json:"deadLettersResolved"`
}

const reconcileMaxAttempts = 5

// Reconcile repairs the differences a crash or Redis loss can leave behind:
// a version reached EVIDENCE_READY without the document status following, a
// stage lost its queue message, an inbox lease outlived its worker, or a dead
// letter was retried successfully. It never invents chunks or vectors; it only
// replays stages that already exist or corrects derived status.
func (s *Service) Reconcile(ctx context.Context, staleAfter time.Duration) (ReconcileReport, error) {
	if staleAfter <= 0 {
		staleAfter = 5 * time.Minute
	}
	report := ReconcileReport{}
	cutoff := time.Now().Add(-staleAfter)

	documents, err := s.repairDocumentStatuses(ctx)
	if err != nil {
		return report, err
	}
	report.DocumentsRepaired = documents
	versions, err := s.repairVersionStatuses(ctx)
	if err != nil {
		return report, err
	}
	report.VersionsRepaired = versions

	inboxes, err := s.expireInboxLeases(ctx, cutoff)
	if err != nil {
		return report, err
	}
	report.InboxesExpired = inboxes
	deadLetters, err := s.resolveRecoveredDeadLetters(ctx)
	if err != nil {
		return report, err
	}
	report.DeadLettersResolved = deadLetters

	requeued, cancelled, err := s.requeueStaleStages(ctx, cutoff)
	if err != nil {
		return report, err
	}
	report.StagesRequeued = requeued
	report.PipelinesCancelled = cancelled
	return report, nil
}

func (s *Service) repairDocumentStatuses(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).Exec(`
UPDATE documents d
SET status = ?, updated_at = NOW()
WHERE d.deleted_at IS NULL
  AND d.status <> ?
  AND d.latest_version_id IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM evidence_packages p
      WHERE p.document_version_id = d.latest_version_id AND p.status = ?
  )`, status.EvidenceReady, status.EvidenceReady, status.EvidenceReady)
	if result.Error != nil {
		return 0, fmt.Errorf("repair document status: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) repairVersionStatuses(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).Exec(`
UPDATE document_versions v
SET status = ?, error_code = '', error_message = '', updated_at = NOW()
WHERE v.status <> ?
  AND EXISTS (
      SELECT 1 FROM evidence_packages p
      WHERE p.document_version_id = v.id AND p.status = ?
  )`, status.EvidenceReady, status.EvidenceReady, status.EvidenceReady)
	if result.Error != nil {
		return 0, fmt.Errorf("repair version status: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) expireInboxLeases(ctx context.Context, cutoff time.Time) (int64, error) {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.TaskInbox{}).
		Where("status = ? AND updated_at < ?", inboxProcessing, cutoff).
		Updates(map[string]any{"status": inboxFailed, "last_error": "reconcile: processing lease expired", "updated_at": now})
	if result.Error != nil {
		return 0, fmt.Errorf("expire inbox leases: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) resolveRecoveredDeadLetters(ctx context.Context) (int64, error) {
	now := time.Now()
	result := s.db.WithContext(ctx).Exec(`
UPDATE task_dead_letters d
SET status = 'RESOLVED', resolved_at = ?, updated_at = ?
WHERE d.status IN ('PENDING', 'RETRYING')
  AND EXISTS (
      SELECT 1 FROM task_inbox i
      WHERE i.idempotency_key = d.idempotency_key AND i.status = 'SUCCEEDED'
  )`, now, now)
	if result.Error != nil {
		return 0, fmt.Errorf("resolve recovered dead letters: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) requeueStaleStages(ctx context.Context, cutoff time.Time) (int, int, error) {
	var stages []models.StageRun
	if err := s.db.WithContext(ctx).
		Joins("JOIN pipeline_runs p ON p.id = stage_runs.pipeline_run_id").
		Where("stage_runs.status IN ? AND stage_runs.updated_at < ? AND p.status NOT IN ?", []string{StagePending, StageProcessing}, cutoff, []string{StatusEvidenceReady, StatusFailed, StatusCancelled}).
		Order("stage_runs.id").Limit(100).Find(&stages).Error; err != nil {
		return 0, 0, fmt.Errorf("list stale stages: %w", err)
	}
	requeued, cancelled := 0, 0
	for _, stage := range stages {
		var pipeline models.PipelineRun
		if err := s.db.WithContext(ctx).First(&pipeline, "id = ?", stage.PipelineRunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return requeued, cancelled, err
		}
		var version models.DocumentVersion
		if err := s.db.WithContext(ctx).First(&version, "id = ?", pipeline.DocumentVersionID).Error; err != nil {
			return requeued, cancelled, fmt.Errorf("load stage version: %w", err)
		}
		var document models.Document
		if err := s.db.WithContext(ctx).First(&document, "id = ?", version.DocumentID).Error; err != nil {
			return requeued, cancelled, fmt.Errorf("load stage document: %w", err)
		}
		if document.DeletedAt != nil || document.LatestVersionID != version.ID {
			if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := cancelStages(tx, pipeline.ID, "superseded"); err != nil {
					return err
				}
				return tx.Model(&models.PipelineRun{}).Where("id = ? AND status NOT IN ?", pipeline.ID, []string{StatusEvidenceReady, StatusFailed, StatusCancelled}).
					Updates(map[string]any{"status": StatusCancelled, "error_code": "superseded", "error_message": "document deleted or version superseded", "updated_at": time.Now()}).Error
			}); err != nil {
				return requeued, cancelled, err
			}
			cancelled++
			continue
		}
		task := taskFromRecords(pipeline, version, stage)
		if stage.Attempts >= reconcileMaxAttempts {
			if err := s.failReconciledStage(ctx, task, "retry_budget_exhausted", "stage retry budget exhausted during reconciliation"); err != nil {
				return requeued, cancelled, err
			}
			continue
		}
		queued, err := s.enqueueReconciledTask(ctx, task)
		if err != nil {
			return requeued, cancelled, err
		}
		if !queued {
			continue
		}
		requeued++
	}
	return requeued, cancelled, nil
}

func (s *Service) enqueueReconciledTask(ctx context.Context, task Task) (bool, error) {
	payload, err := json.Marshal(task)
	if err != nil {
		return false, err
	}
	now := time.Now()
	queued := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event models.OutboxEvent
		lookupErr := tx.Where("idempotency_key = ?", task.IdempotencyKey).First(&event).Error
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			if err := tx.Create(&models.OutboxEvent{TenantID: task.TenantID, AggregateType: "pipeline_run", AggregateID: task.PipelineRunID, EventType: "document." + task.Stage, IdempotencyKey: task.IdempotencyKey, Payload: string(payload)}).Error; err != nil {
				return err
			}
			queued = true
		} else if event.PublishedAt != nil {
			if err := tx.Model(&event).Updates(map[string]any{"published_at": nil, "attempts": 0, "next_attempt_at": nil, "last_error": ""}).Error; err != nil {
				return err
			}
			queued = true
		}
		if !queued {
			return nil
		}
		return tx.Model(&models.StageRun{}).Where("idempotency_key = ? AND status = ?", task.IdempotencyKey, StageProcessing).
			Updates(map[string]any{"status": StagePending, "updated_at": now}).Error
	})
	if err != nil {
		return false, fmt.Errorf("enqueue reconciled task: %w", err)
	}
	return queued, nil
}

func (s *Service) failReconciledStage(ctx context.Context, task Task, code, message string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&models.StageRun{}).Where("idempotency_key = ?", task.IdempotencyKey).Updates(map[string]any{
			"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.PipelineRun{}).Where("id = ? AND status NOT IN ?", task.PipelineRunID, []string{StatusEvidenceReady, StatusCancelled}).
			Updates(map[string]any{"status": StatusFailed, "error_code": code, "error_message": message, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DocumentVersion{}).Where("id = ? AND status <> ?", task.DocumentVersionID, StatusCancelled).
			Updates(map[string]any{"status": StatusFailed, "error_code": code, "error_message": message, "updated_at": now}).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(task)
		var existing models.TaskDeadLetter
		err := tx.Where("idempotency_key = ?", task.IdempotencyKey).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&models.TaskDeadLetter{
				IdempotencyKey: task.IdempotencyKey, TenantID: task.TenantID, KnowledgeBaseID: task.KnowledgeBaseID,
				DocumentVersionID: task.DocumentVersionID, TaskType: task.TaskType, Stage: task.Stage,
				Attempts: reconcileMaxAttempts, LastError: message, Payload: string(payload), Status: deadLetterPending,
			}).Error
		}
		return err
	})
}

func taskFromRecords(pipeline models.PipelineRun, version models.DocumentVersion, stage models.StageRun) Task {
	return Task{
		Envelope: Envelope{
			SchemaVersion: SchemaTask, TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID,
			DocumentID: version.DocumentID, DocumentVersionID: version.ID, PipelineRunID: pipeline.ID,
			StageRunID: fmt.Sprint(stage.ID), TraceID: pipeline.TraceID, RequestID: pipeline.RequestID,
			IdempotencyKey: stage.IdempotencyKey, ContentHash: version.ContentHash, CreatedAt: time.Now().UTC(),
		},
		TaskType: TypeTask, Stage: stage.Stage, ObjectKey: version.SourceObjectKey,
		PackageID: stableID("pkg", version.ID),
	}
}
