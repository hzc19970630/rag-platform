package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/pipeline"
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
	return s.reconcile(ctx, "", staleAfter)
}

// ReconcileTenant is the operator-facing pass: it applies the same repairs but
// only to rows owned by tenantID. StageRun and TaskInbox carry no tenant column,
// so they are scoped through the pipeline run or projection run they belong to.
func (s *Service) ReconcileTenant(ctx context.Context, tenantID string, staleAfter time.Duration) (ReconcileReport, error) {
	if tenantID == "" {
		return ReconcileReport{}, fmt.Errorf("tenant is required")
	}
	return s.reconcile(ctx, tenantID, staleAfter)
}

func (s *Service) reconcile(ctx context.Context, tenantID string, staleAfter time.Duration) (ReconcileReport, error) {
	if staleAfter <= 0 {
		staleAfter = 5 * time.Minute
	}
	report := ReconcileReport{}
	now := time.Now()
	cutoff := now.Add(-staleAfter)

	documents, err := s.repairDocumentStatuses(ctx, tenantID, now)
	if err != nil {
		return report, err
	}
	report.DocumentsRepaired = documents
	versions, err := s.repairVersionStatuses(ctx, tenantID, now)
	if err != nil {
		return report, err
	}
	report.VersionsRepaired = versions

	inboxes, err := s.expireInboxLeases(ctx, tenantID, now)
	if err != nil {
		return report, err
	}
	report.InboxesExpired = inboxes
	deadLetters, err := s.resolveRecoveredDeadLetters(ctx, tenantID, now)
	if err != nil {
		return report, err
	}
	report.DeadLettersResolved = deadLetters

	requeued, cancelled, err := s.requeueStaleStages(ctx, tenantID, cutoff)
	if err != nil {
		return report, err
	}
	report.StagesRequeued = requeued
	report.PipelinesCancelled = cancelled
	return report, nil
}

func (s *Service) repairDocumentStatuses(ctx context.Context, tenantID string, now time.Time) (int64, error) {
	tenantFilter := ""
	args := []any{status.EvidenceReady, now}
	if tenantID != "" {
		tenantFilter = "AND documents.tenant_id = ?"
		args = append(args, tenantID)
	}
	args = append(args, status.EvidenceReady, status.EvidenceReady)
	result := s.db.WithContext(ctx).Exec(`
UPDATE documents
SET status = ?, updated_at = ?
WHERE documents.deleted_at IS NULL
  `+tenantFilter+`
  AND documents.status <> ?
  AND documents.latest_version_id IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM evidence_packages p
      WHERE p.document_version_id = documents.latest_version_id AND p.status = ?
  )`, args...)
	if result.Error != nil {
		return 0, fmt.Errorf("repair document status: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) repairVersionStatuses(ctx context.Context, tenantID string, now time.Time) (int64, error) {
	tenantFilter := ""
	args := []any{status.EvidenceReady, now, status.EvidenceReady}
	if tenantID != "" {
		tenantFilter = "AND document_versions.tenant_id = ?"
		args = append(args, tenantID)
	}
	args = append(args, status.EvidenceReady)
	result := s.db.WithContext(ctx).Exec(`
UPDATE document_versions
SET status = ?, error_code = '', error_message = '', updated_at = ?
WHERE document_versions.status <> ? `+tenantFilter+`
  AND EXISTS (
      SELECT 1 FROM evidence_packages p
      WHERE p.document_version_id = document_versions.id AND p.status = ?
  )`, args...)
	if result.Error != nil {
		return 0, fmt.Errorf("repair version status: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// expireInboxLeases reclaims only rows whose lease actually expired. Row age
// (updated_at) is not evidence of a dead worker: a live worker keeps renewing
// lease_until, and a task with an old updated_at may still be heartbeating.
func (s *Service) expireInboxLeases(ctx context.Context, tenantID string, now time.Time) (int64, error) {
	query := s.db.WithContext(ctx).Model(&models.TaskInbox{}).
		Where("status = ? AND lease_until IS NOT NULL AND lease_until <= ?", inboxProcessing, now)
	if tenantID != "" {
		query = query.Where(`(EXISTS (
    SELECT 1 FROM stage_runs sr
    JOIN pipeline_runs pr ON pr.id = sr.pipeline_run_id
    WHERE sr.idempotency_key = task_inbox.idempotency_key AND pr.tenant_id = ?
) OR EXISTS (
    SELECT 1 FROM projection_stage_runs psr
    JOIN projection_runs prj ON prj.id = psr.projection_run_id
    WHERE psr.idempotency_key = task_inbox.idempotency_key AND prj.tenant_id = ?
))`, tenantID, tenantID)
	}
	result := query.Updates(map[string]any{
		"status": inboxFailed, "lease_owner": "", "lease_until": nil,
		"last_error": "reconcile: processing lease expired", "updated_at": now,
	})
	if result.Error != nil {
		return 0, fmt.Errorf("expire inbox leases: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) resolveRecoveredDeadLetters(ctx context.Context, tenantID string, now time.Time) (int64, error) {
	tenantFilter := ""
	args := []any{now, now}
	if tenantID != "" {
		tenantFilter = "AND task_dead_letters.tenant_id = ?"
		args = append(args, tenantID)
	}
	result := s.db.WithContext(ctx).Exec(`
UPDATE task_dead_letters
SET status = 'RESOLVED', resolved_at = ?, updated_at = ?
WHERE task_dead_letters.status IN ('PENDING', 'RETRYING') `+tenantFilter+`
  AND EXISTS (
      SELECT 1 FROM task_inbox i
      WHERE i.idempotency_key = task_dead_letters.idempotency_key AND i.status = 'SUCCEEDED'
  )`, args...)
	if result.Error != nil {
		return 0, fmt.Errorf("resolve recovered dead letters: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) requeueStaleStages(ctx context.Context, tenantID string, cutoff time.Time) (int, int, error) {
	query := s.db.WithContext(ctx).
		Joins("JOIN pipeline_runs p ON p.id = stage_runs.pipeline_run_id").
		Where("stage_runs.status IN ? AND stage_runs.updated_at < ? AND p.status NOT IN ?", []string{StagePending, StageProcessing}, cutoff, []string{StatusEvidenceReady, StatusFailed, StatusCancelled})
	if tenantID != "" {
		query = query.Where("p.tenant_id = ?", tenantID)
	}
	var stages []models.StageRun
	if err := query.Order("stage_runs.id").Limit(100).Find(&stages).Error; err != nil {
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
		requeued, err := pipeline.RequeueEvent(tx, models.OutboxEvent{
			TenantID: task.TenantID, AggregateType: "pipeline_run", AggregateID: task.PipelineRunID,
			EventType: "document." + task.Stage, IdempotencyKey: task.IdempotencyKey, Payload: string(payload),
		})
		if err != nil {
			return err
		}
		queued = requeued
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
		if err := tx.Model(&models.PipelineRun{}).Where("id = ? AND tenant_id = ? AND status NOT IN ?", task.PipelineRunID, task.TenantID, []string{StatusEvidenceReady, StatusCancelled}).
			Updates(map[string]any{"status": StatusFailed, "error_code": code, "error_message": message, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DocumentVersion{}).Where("id = ? AND tenant_id = ? AND status <> ?", task.DocumentVersionID, task.TenantID, StatusCancelled).
			Updates(map[string]any{"status": StatusFailed, "error_code": code, "error_message": message, "updated_at": now}).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(task)
		return pipeline.UpsertDeadLetter(tx, models.TaskDeadLetter{
			IdempotencyKey: task.IdempotencyKey, TenantID: task.TenantID, KnowledgeBaseID: task.KnowledgeBaseID,
			DocumentVersionID: task.DocumentVersionID, TaskType: task.TaskType, Stage: task.Stage,
			Attempts: reconcileMaxAttempts, LastError: message, Payload: string(payload), Status: pipeline.DeadLetterPending,
		})
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
