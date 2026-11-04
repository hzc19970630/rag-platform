package knowledgeprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	inboxProcessing = "PROCESSING"
	inboxSucceeded  = "SUCCEEDED"
	inboxFailed     = "FAILED"
)

func stageStatusFor(projectionType, stage string) string {
	if projectionType == ProjectionTypeRelation {
		if stage == StageCollect {
			return RelationPending
		}
		return RelationExtracting
	}
	return stageRunStatus(stage)
}

func nextStageFor(projectionType, stage string) (string, bool) {
	if projectionType == ProjectionTypeRelation {
		switch stage {
		case StageCollect:
			return StageExtract, true
		case StageExtract:
			return StageFinalize, true
		default:
			return "", false
		}
	}
	return nextStage(stage)
}

func failedStatusFor(projectionType string) string {
	if projectionType == ProjectionTypeRelation {
		return RelationFailed
	}
	return ProjectionFailed
}

func retryStatusFor(projectionType string) string {
	if projectionType == ProjectionTypeRelation {
		return RelationRetrying
	}
	return ProjectionRetrying
}

func readyStatusFor(projectionType string) string {
	if projectionType == ProjectionTypeRelation {
		return RelationReady
	}
	return ProjectionReady
}

// Process executes one projection stage. Only ids travel in the task; the
// worker loads the immutable EvidencePackage JSON for content.
func (s *Service) Process(ctx context.Context, task Task) error {
	switch task.ProjectionType {
	case ProjectionTypeWiki:
		return s.processWiki(ctx, task)
	case ProjectionTypeRelation:
		return s.processRelation(ctx, task)
	default:
		return apperror.Terminalf("unsupported_projection_type", "unsupported projection type %q", task.ProjectionType)
	}
}

func (s *Service) completeStage(ctx context.Context, task Task, run *models.ProjectionRun, stage *models.ProjectionStageRun) error {
	now := time.Now()
	next, hasNext := nextStageFor(run.ProjectionType, task.Stage)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ProjectionStageRun{}).Where("id = ?", stage.ID).Updates(map[string]any{
			"status": StageSucceeded, "completed_at": &now, "error_code": "", "error_message": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", task.IdempotencyKey).Updates(map[string]any{
			"status": inboxSucceeded, "processed_at": &now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if !hasNext {
			return nil
		}
		nextKey := run.ID + ":" + next
		var nextStageRow models.ProjectionStageRun
		if err := tx.Where("idempotency_key = ?", nextKey).First(&nextStageRow).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			nextStageRow = models.ProjectionStageRun{
				ProjectionRunID: run.ID, ProjectionType: run.ProjectionType, Stage: next,
				Status: StagePending, IdempotencyKey: nextKey,
			}
			if err := tx.Create(&nextStageRow).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return enqueueStage(tx, run, next, nextStageRow.Attempts)
	})
}

func enqueueStage(tx *gorm.DB, run *models.ProjectionRun, stage string, attempt int) error {
	key := run.ID + ":" + stage
	schema, eventType := SchemaWikiTask, "projection.wiki"
	if run.ProjectionType == ProjectionTypeRelation {
		schema, eventType = SchemaRelationTask, "projection.relation"
	}
	task := Task{
		SchemaVersion: schema, TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID,
		EvidencePackageID: run.EvidencePackageID, ProjectionRunID: run.ID,
		ProjectionType: run.ProjectionType, Stage: stage, IdempotencyKey: key, Attempt: attempt,
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	var event models.OutboxEvent
	lookupErr := tx.Where("idempotency_key = ?", key).First(&event).Error
	if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return tx.Create(&models.OutboxEvent{
			TenantID: run.TenantID, AggregateType: "projection_run", AggregateID: run.ID,
			EventType: eventType, IdempotencyKey: key, Payload: string(payload),
		}).Error
	}
	if lookupErr != nil {
		return lookupErr
	}
	// A retry re-opens the same durable event. The task attempt changes the queue
	// id so Asynq's retained completed record cannot suppress the retry.
	return tx.Model(&event).Updates(map[string]any{
		"payload": string(payload), "published_at": nil, "attempts": 0, "next_attempt_at": nil, "last_error": "",
	}).Error
}

func (s *Service) claimInbox(ctx context.Context, task Task) (bool, error) {
	now := time.Now()
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskInbox
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "idempotency_key = ?", task.IdempotencyKey).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = models.TaskInbox{IdempotencyKey: task.IdempotencyKey, TaskType: "projection." + task.ProjectionType + "." + task.Stage, Status: inboxProcessing, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			claimed = true
			return nil
		}
		if err != nil {
			return err
		}
		if row.Status == inboxSucceeded {
			return nil
		}
		if err := tx.Model(&row).Updates(map[string]any{"status": inboxProcessing, "last_error": "", "updated_at": now}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

func (s *Service) markStageProcessing(ctx context.Context, task Task, run *models.ProjectionRun) (*models.ProjectionStageRun, error) {
	now := time.Now()
	var stage models.ProjectionStageRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stage, "idempotency_key = ?", task.IdempotencyKey).Error; err != nil {
			return err
		}
		if stage.ProjectionRunID != run.ID || stage.Stage != task.Stage || stage.ProjectionType != run.ProjectionType {
			return fmt.Errorf("projection stage does not belong to run")
		}
		runStatus := stageStatusFor(run.ProjectionType, task.Stage)
		if err := tx.Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", run.ID, ProjectionCancelled).Updates(map[string]any{
			"status": runStatus, "error_code": "", "error_message": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.ProjectionStageRun{}).Where("id = ?", stage.ID).Updates(map[string]any{
			"status": StageProcessing, "attempts": gorm.Expr("attempts + 1"), "started_at": &now,
			"error_code": "", "error_message": "", "updated_at": now,
		}).Error
	})
	return &stage, err
}

func (s *Service) failStage(ctx context.Context, run *models.ProjectionRun, stage *models.ProjectionStageRun, processErr error) error {
	code, message := splitError(processErr)
	now := time.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ProjectionStageRun{}).Where("id = ?", stage.ID).Updates(map[string]any{
			"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", run.ID, ProjectionCancelled).Updates(map[string]any{
			"status": failedStatusFor(run.ProjectionType), "error_code": code, "error_message": message, "updated_at": now,
		}).Error
	})
}

func (s *Service) finishInbox(ctx context.Context, key, status string, processErr error) error {
	values := map[string]any{"status": status, "updated_at": time.Now()}
	if status == inboxSucceeded || status == inboxFailed {
		values["processed_at"] = time.Now()
	}
	if processErr != nil {
		values["last_error"] = processErr.Error()
	}
	return s.db.WithContext(ctx).Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).Updates(values).Error
}

func (s *Service) loadRun(ctx context.Context, id string) (*models.ProjectionRun, error) {
	var run models.ProjectionRun
	if err := s.db.WithContext(ctx).First(&run, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("load projection run: %w", err)
	}
	return &run, nil
}

func (s *Service) loadKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string) (models.KnowledgeBase, error) {
	var knowledgeBase models.KnowledgeBase
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, knowledgeBaseID).First(&knowledgeBase).Error
	return knowledgeBase, err
}

// loadCurrentPackages returns only evidence packages that still describe the
// current, evidence-ready, non-deleted document version. Projection consumes
// nothing else.
func (s *Service) loadCurrentPackages(ctx context.Context, tenantID, knowledgeBaseID string) ([]ingestion.EvidencePackage, error) {
	if s.evidence == nil {
		return nil, fmt.Errorf("evidence reader is not configured")
	}
	return s.evidence.CurrentPackages(ctx, tenantID, knowledgeBaseID)
}

func packagesSourceHash(packages []ingestion.EvidencePackage, aclVersion int64, promptVersion, model string) (string, error) {
	digest := sha256.New()
	fmt.Fprintf(digest, "acl=%d\nprompt=%s\nmodel=%s\n", aclVersion, promptVersion, model)
	for _, pkg := range packages {
		fmt.Fprintf(digest, "%s=%s\n", pkg.ID, pkg.ContentHash)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func splitError(err error) (string, string) {
	if terminal, ok := apperror.AsTerminal(err); ok {
		message := ""
		if terminal.Err != nil {
			message = terminal.Err.Error()
		}
		return terminal.Code, message
	}
	value := err.Error()
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 1 {
		return "projection_error", strings.TrimSpace(parts[0])
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:])[:48]
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
