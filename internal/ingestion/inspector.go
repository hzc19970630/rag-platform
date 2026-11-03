package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const (
	deadLetterPending   = "PENDING"
	deadLetterRetrying  = "RETRYING"
	deadLetterResolved  = "RESOLVED"
	deadLetterDiscarded = "DISCARDED"
)

// TaskInspector is the operator view over one ingestion task: the inbox lease,
// pipeline/stage state, outbox publication and dead-letter record. Retry and
// discard are explicit, audited actions instead of silent requeues.
type TaskInspector struct {
	db        *gorm.DB
	publisher TaskPublisher
}

func NewTaskInspector(db *gorm.DB, publisher TaskPublisher) *TaskInspector {
	return &TaskInspector{db: db, publisher: publisher}
}

type TaskInspection struct {
	Task       Task                   `json:"task"`
	Inbox      *models.TaskInbox      `json:"inbox,omitempty"`
	Pipeline   *models.PipelineRun    `json:"pipeline,omitempty"`
	Stage      *models.StageRun       `json:"stage,omitempty"`
	Outbox     []models.OutboxEvent   `json:"outbox"`
	DeadLetter *models.TaskDeadLetter `json:"deadLetter,omitempty"`
}

func (i *TaskInspector) Inspect(ctx context.Context, principal authz.Principal, idempotencyKey string) (TaskInspection, error) {
	if err := requireTenantOperator(ctx, i.db, principal); err != nil {
		return TaskInspection{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return TaskInspection{}, fmt.Errorf("idempotency key is required")
	}
	inspection := TaskInspection{Outbox: []models.OutboxEvent{}}
	var inbox models.TaskInbox
	if err := i.db.WithContext(ctx).First(&inbox, "idempotency_key = ?", idempotencyKey).Error; err == nil {
		inspection.Inbox = &inbox
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task inbox: %w", err)
	}
	var stage models.StageRun
	if err := i.db.WithContext(ctx).First(&stage, "idempotency_key = ?", idempotencyKey).Error; err == nil {
		inspection.Stage = &stage
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task stage: %w", err)
	}
	if inspection.Stage != nil {
		var pipeline models.PipelineRun
		if err := i.db.WithContext(ctx).First(&pipeline, "id = ?", inspection.Stage.PipelineRunID).Error; err == nil {
			if pipeline.TenantID != principal.TenantID {
				return TaskInspection{}, apperror.ErrNotFound
			}
			inspection.Pipeline = &pipeline
		}
	}
	var event models.OutboxEvent
	if err := i.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).First(&event).Error; err == nil {
		if event.TenantID != principal.TenantID {
			return TaskInspection{}, apperror.ErrNotFound
		}
		inspection.Outbox = append(inspection.Outbox, event)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task outbox event: %w", err)
	}
	var dead models.TaskDeadLetter
	if err := i.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).First(&dead).Error; err == nil {
		if dead.TenantID != principal.TenantID {
			return TaskInspection{}, apperror.ErrNotFound
		}
		inspection.DeadLetter = &dead
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task dead letter: %w", err)
	}
	if inspection.Pipeline == nil && inspection.Inbox == nil && inspection.DeadLetter == nil {
		return TaskInspection{}, apperror.ErrNotFound
	}
	if inspection.Pipeline != nil && inspection.Pipeline.TenantID != principal.TenantID {
		return TaskInspection{}, apperror.ErrNotFound
	}
	payload := event.Payload
	if payload == "" && inspection.DeadLetter != nil {
		payload = inspection.DeadLetter.Payload
	}
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &inspection.Task)
	}
	return inspection, nil
}

func (i *TaskInspector) ListDeadLetters(ctx context.Context, principal authz.Principal, status string, limit int) ([]models.TaskDeadLetter, error) {
	if err := requireTenantOperator(ctx, i.db, principal); err != nil {
		return nil, err
	}
	query := i.db.WithContext(ctx).Where("tenant_id = ?", principal.TenantID)
	if status != "" {
		query = query.Where("status = ?", strings.ToUpper(status))
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []models.TaskDeadLetter
	if err := query.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list dead letters: %w", err)
	}
	return rows, nil
}

// RecordDeadLetter is called by the queue error handler once retries are
// exhausted. It is idempotent on the stage idempotency key.
func (i *TaskInspector) RecordDeadLetter(ctx context.Context, task Task, taskErr error) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	attempts := 0
	if task.StageRunID != "" {
		var stage models.StageRun
		if err := i.db.WithContext(ctx).Where("idempotency_key = ?", task.IdempotencyKey).First(&stage).Error; err == nil {
			attempts = stage.Attempts
		}
	}
	message := ""
	if taskErr != nil {
		message = taskErr.Error()
	}
	now := time.Now()
	row := models.TaskDeadLetter{
		IdempotencyKey: task.IdempotencyKey, TenantID: task.TenantID, KnowledgeBaseID: task.KnowledgeBaseID,
		DocumentVersionID: task.DocumentVersionID, TaskType: task.TaskType, Stage: task.Stage,
		Attempts: attempts, LastError: message, Payload: string(payload), Status: deadLetterPending,
		CreatedAt: now, UpdatedAt: now,
	}
	var existing models.TaskDeadLetter
	err = i.db.WithContext(ctx).Where("idempotency_key = ?", task.IdempotencyKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = i.db.WithContext(ctx).Create(&row).Error
	} else if err != nil {
		return err
	} else if existing.Status == deadLetterResolved {
		return nil
	} else {
		err = i.db.WithContext(ctx).Model(&existing).Updates(map[string]any{
			"attempts": attempts, "last_error": message, "payload": string(payload), "status": deadLetterPending, "updated_at": now,
		}).Error
	}
	if err != nil {
		return err
	}
	// The retry budget is spent. Release the inbox lease as FAILED so an operator
	// retry can claim it again; PROCESSING would look like a live worker.
	return i.db.WithContext(ctx).Model(&models.TaskInbox{}).
		Where("idempotency_key = ? AND status = ?", task.IdempotencyKey, inboxProcessing).
		Updates(map[string]any{"status": inboxFailed, "last_error": message, "updated_at": now}).Error
}

func (i *TaskInspector) RetryDeadLetter(ctx context.Context, principal authz.Principal, id uint64) error {
	if err := requireTenantOperator(ctx, i.db, principal); err != nil {
		return err
	}
	var row models.TaskDeadLetter
	if err := i.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load dead letter: %w", err)
	}
	if row.Status == deadLetterResolved {
		return fmt.Errorf("dead letter is already resolved")
	}
	if row.Status == deadLetterRetrying {
		return fmt.Errorf("%w: dead letter retry is already in progress", apperror.ErrConflict)
	}
	var task Task
	if err := json.Unmarshal([]byte(row.Payload), &task); err != nil {
		return fmt.Errorf("decode dead letter payload: %w", err)
	}
	// The archived task belongs to the pool that exhausted its retries, so it
	// has to be removed before the task is requeued elsewhere.
	if err := i.publisher.RemoveArchived(ctx, task); err != nil {
		return err
	}
	// Operator retries use the interactive pool: they must not queue behind a
	// bulk ingestion backlog.
	task.Queue = "interactive"
	if err := i.publisher.Publish(ctx, task); err != nil {
		return err
	}
	now := time.Now()
	if err := i.db.WithContext(ctx).Model(&row).Updates(map[string]any{
		"status": deadLetterRetrying, "resolved_by": principal.SubjectID, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := i.db.WithContext(ctx).Model(&models.TaskInbox{}).Where("idempotency_key = ?", row.IdempotencyKey).Updates(map[string]any{
		"status": inboxFailed, "last_error": "", "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := i.db.WithContext(ctx).Model(&models.StageRun{}).Where("idempotency_key = ?", row.IdempotencyKey).Updates(map[string]any{
		"status": StagePending, "error_code": "", "error_message": "", "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return i.recordOperatorAudit(ctx, principal, "retry", "dead_letter", fmt.Sprint(row.ID), row.IdempotencyKey)
}

func (i *TaskInspector) DiscardDeadLetter(ctx context.Context, principal authz.Principal, id uint64) error {
	if err := requireTenantOperator(ctx, i.db, principal); err != nil {
		return err
	}
	var row models.TaskDeadLetter
	if err := i.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load dead letter: %w", err)
	}
	var task Task
	if err := json.Unmarshal([]byte(row.Payload), &task); err != nil {
		return fmt.Errorf("decode dead letter payload: %w", err)
	}
	if err := i.publisher.RemoveArchived(ctx, task); err != nil {
		return err
	}
	now := time.Now()
	if err := i.db.WithContext(ctx).Model(&row).Updates(map[string]any{
		"status": deadLetterDiscarded, "resolved_at": &now, "resolved_by": principal.SubjectID, "updated_at": now,
	}).Error; err != nil {
		return fmt.Errorf("discard dead letter: %w", err)
	}
	return i.recordOperatorAudit(ctx, principal, "discard", "dead_letter", fmt.Sprint(id), "")
}

// CancelPipeline is the manual cancel action. It stops future stage delivery and
// records why; the worker also re-checks cancellation before each stage.
func (i *TaskInspector) CancelPipeline(ctx context.Context, principal authz.Principal, pipelineID string) error {
	if err := requireTenantOperator(ctx, i.db, principal); err != nil {
		return err
	}
	var pipeline models.PipelineRun
	if err := i.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, pipelineID).First(&pipeline).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load pipeline: %w", err)
	}
	if err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := cancelStages(tx, pipeline.ID, "cancelled by operator"); err != nil {
			return err
		}
		return tx.Model(&pipeline).Updates(map[string]any{"status": StatusCancelled, "error_code": "cancelled", "error_message": "cancelled by operator", "updated_at": time.Now()}).Error
	}); err != nil {
		return fmt.Errorf("cancel pipeline: %w", err)
	}
	return i.recordOperatorAudit(ctx, principal, "cancel", "pipeline", pipeline.ID, "")
}

func (i *TaskInspector) recordOperatorAudit(ctx context.Context, principal authz.Principal, action, resourceType, resourceID, reason string) error {
	return i.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action, ResourceType: resourceType, ResourceID: resourceID, Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func cancelStages(tx *gorm.DB, pipelineID, reason string) error {
	now := time.Now()
	return tx.Model(&models.StageRun{}).Where("pipeline_run_id = ? AND status IN ?", pipelineID, []string{StagePending, StageProcessing}).Updates(map[string]any{
		"status": StageCancelled, "error_code": "cancelled", "error_message": reason, "updated_at": now,
	}).Error
}

func requireTenantOperator(ctx context.Context, db *gorm.DB, principal authz.Principal) error {
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.SubjectID) == "" {
		return apperror.ErrUnauthenticated
	}
	if principal.CredentialType == "api_key" {
		return apperror.ErrForbidden
	}
	var role string
	if err := db.WithContext(ctx).Table("memberships").Select("role").Where("tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Scan(&role).Error; err != nil {
		return fmt.Errorf("load operator membership: %w", err)
	}
	if role != "owner" && role != "admin" {
		return apperror.ErrForbidden
	}
	return nil
}
