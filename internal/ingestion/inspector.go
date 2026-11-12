package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
)

const (
	deadLetterPending   = pipeline.DeadLetterPending
	deadLetterRetrying  = pipeline.DeadLetterRetrying
	deadLetterResolved  = pipeline.DeadLetterResolved
	deadLetterDiscarded = pipeline.DeadLetterDiscarded
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

type MechanismState struct {
	Mechanism     string     `json:"mechanism"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts,omitempty"`
	LeaseUntil    *time.Time `json:"leaseUntil,omitempty"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	Detail        string     `json:"detail,omitempty"`
}

type TaskInspection struct {
	Task       any                    `json:"task"`
	Kind       string                 `json:"kind"`
	Mechanisms []MechanismState       `json:"mechanisms"`
	Inbox      *models.TaskInbox      `json:"inbox,omitempty"`
	Pipeline   *models.PipelineRun    `json:"pipeline,omitempty"`
	Stage      *models.StageRun       `json:"stage,omitempty"`
	Outbox     []models.OutboxEvent   `json:"outbox"`
	DeadLetter *models.TaskDeadLetter `json:"deadLetter,omitempty"`
}

// Inspect resolves one idempotency key across ingestion, projection, connector
// sync and durable answer jobs. Each mechanism keeps its own enforcer; this view
// only reads and normalizes their persisted state.
func (i *TaskInspector) Inspect(ctx context.Context, principal authz.Principal, idempotencyKey string) (TaskInspection, error) {
	if err := requireTenantOperator(ctx, i.db, principal); err != nil {
		return TaskInspection{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return TaskInspection{}, apperror.Invalidf("idempotency key is required")
	}
	inspection := TaskInspection{Outbox: []models.OutboxEvent{}, Mechanisms: []MechanismState{}}
	var inbox models.TaskInbox
	inboxFound := false
	if err := i.db.WithContext(ctx).First(&inbox, "idempotency_key = ?", idempotencyKey).Error; err == nil {
		inboxFound = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task inbox: %w", err)
	}
	var stage models.StageRun
	stageFound := false
	if err := i.db.WithContext(ctx).First(&stage, "idempotency_key = ?", idempotencyKey).Error; err == nil {
		stageFound = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task stage: %w", err)
	}
	if stageFound {
		var pipeline models.PipelineRun
		if err := tenantdb.MustScoped(i.db.WithContext(ctx), principal.TenantID).First(&pipeline, "id = ?", stage.PipelineRunID).Error; err == nil {
			inspection.Pipeline = &pipeline
		}
	}
	var dead models.TaskDeadLetter
	if err := tenantdb.MustScoped(i.db.WithContext(ctx), principal.TenantID).Where("idempotency_key = ?", idempotencyKey).First(&dead).Error; err == nil {
		inspection.DeadLetter = &dead
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TaskInspection{}, fmt.Errorf("load task dead letter: %w", err)
	}
	outboxKeys := []string{idempotencyKey}
	kind := ""
	var projectionStage *models.ProjectionStageRun
	var projectionRun *models.ProjectionRun
	var syncRun *models.SyncRun
	var answerJob *models.AnswerJob

	baseEvents, err := i.loadOutboxEvents(ctx, principal.TenantID, outboxKeys)
	if err != nil {
		return TaskInspection{}, err
	}
	inspection.Outbox = baseEvents
	// TaskInbox and StageRun have no tenant column. An inbox row is only
	// evidence of this tenant's task when a tenant-owned pipeline, outbox event
	// or dead letter carries the same idempotency key.
	owned := inspection.Pipeline != nil || inspection.DeadLetter != nil || len(baseEvents) > 0
	if owned {
		kind = "ingestion"
		// A projection outbox event is tenant-owned but is not an ingestion
		// pipeline. Classify by aggregate type before falling back.
		if inspection.Pipeline == nil && len(baseEvents) > 0 && baseEvents[0].AggregateType == "projection_run" {
			projectionStage, projectionRun, err = i.loadProjection(ctx, principal.TenantID, idempotencyKey)
			if err != nil {
				return TaskInspection{}, err
			}
			if projectionRun != nil {
				kind = "projection"
			}
		}
	} else {
		var err error
		projectionStage, projectionRun, err = i.loadProjection(ctx, principal.TenantID, idempotencyKey)
		if err != nil {
			return TaskInspection{}, err
		}
		if projectionRun != nil {
			kind = "projection"
			owned = true
		}
	}
	if !owned && strings.HasPrefix(idempotencyKey, "connector-sync:") {
		runID := strings.TrimPrefix(idempotencyKey, "connector-sync:")
		var run models.SyncRun
		if err := tenantdb.MustScoped(i.db.WithContext(ctx), principal.TenantID).First(&run, "id = ?", runID).Error; err == nil {
			syncRun = &run
			kind = "sync"
			owned = true
			outboxKeys = append(outboxKeys, "sync-outbox:"+runID)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return TaskInspection{}, fmt.Errorf("load sync run: %w", err)
		}
	}
	if !owned {
		var job models.AnswerJob
		if err := tenantdb.MustScoped(i.db.WithContext(ctx), principal.TenantID).First(&job, "id = ?", idempotencyKey).Error; err == nil {
			answerJob = &job
			kind = "answer"
			owned = true
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return TaskInspection{}, fmt.Errorf("load answer job: %w", err)
		}
	}
	if !owned {
		return TaskInspection{}, apperror.ErrNotFound
	}
	if len(outboxKeys) > 1 {
		events, err := i.loadOutboxEvents(ctx, principal.TenantID, outboxKeys)
		if err != nil {
			return TaskInspection{}, err
		}
		inspection.Outbox = events
	}
	events := inspection.Outbox
	if stageFound {
		inspection.Stage = &stage
	}
	if inboxFound {
		inspection.Inbox = &inbox
	}
	var payload string
	if len(events) > 0 {
		payload = events[0].Payload
	}
	if payload == "" && inspection.DeadLetter != nil {
		payload = inspection.DeadLetter.Payload
	}
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &inspection.Task)
	}
	inspection.Kind = kind
	inspection.Mechanisms = buildMechanisms(&inbox, &stage, events, inspection.DeadLetter, projectionStage, projectionRun, syncRun, answerJob)
	return inspection, nil
}

func (i *TaskInspector) loadProjection(ctx context.Context, tenantID, key string) (*models.ProjectionStageRun, *models.ProjectionRun, error) {
	var stage models.ProjectionStageRun
	if err := i.db.WithContext(ctx).First(&stage, "idempotency_key = ?", key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("load projection stage: %w", err)
	}
	var run models.ProjectionRun
	if err := tenantdb.MustScoped(i.db.WithContext(ctx), tenantID).First(&run, "id = ?", stage.ProjectionRunID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("load projection run: %w", err)
	}
	return &stage, &run, nil
}

func (i *TaskInspector) loadOutboxEvents(ctx context.Context, tenantID string, keys []string) ([]models.OutboxEvent, error) {
	var events []models.OutboxEvent
	if err := tenantdb.MustScoped(i.db.WithContext(ctx), tenantID).Where("idempotency_key IN ?", keys).Order("id").Find(&events).Error; err != nil {
		return nil, fmt.Errorf("load task outbox events: %w", err)
	}
	return events, nil
}

func buildMechanisms(inbox *models.TaskInbox, stage *models.StageRun, outbox []models.OutboxEvent, dead *models.TaskDeadLetter, projectionStage *models.ProjectionStageRun, projectionRun *models.ProjectionRun, syncRun *models.SyncRun, answerJob *models.AnswerJob) []MechanismState {
	result := make([]MechanismState, 0, 8)
	for _, event := range outbox {
		state := status.Pending
		if event.PublishedAt != nil {
			state = status.Published
		}
		result = append(result, MechanismState{Mechanism: status.MechanismOutboxAttempts, Status: state, Attempts: event.Attempts, NextAttemptAt: event.NextAttemptAt, LastError: event.LastError, Detail: event.EventType})
	}
	if inbox != nil && inbox.IdempotencyKey != "" {
		result = append(result, MechanismState{Mechanism: status.MechanismInboxLease, Status: inbox.Status, LeaseUntil: inbox.LeaseUntil, LastError: inbox.LastError})
	}
	if stage != nil && stage.IdempotencyKey != "" {
		result = append(result, MechanismState{Mechanism: status.MechanismStageAttempts, Status: stage.Status, Attempts: stage.Attempts, LastError: stage.ErrorMessage, Detail: stage.Stage})
	}
	if dead != nil {
		result = append(result, MechanismState{Mechanism: status.MechanismAsynqRetry, Status: dead.Status, Attempts: dead.Attempts, LastError: dead.LastError, Detail: "archived task with a dead letter"})
	}
	if projectionStage != nil {
		result = append(result, MechanismState{Mechanism: status.MechanismProjectionRetry, Status: projectionStage.Status, Attempts: projectionStage.Attempts, LastError: projectionStage.ErrorMessage, Detail: projectionStage.Stage})
	}
	if projectionRun != nil {
		result = append(result, MechanismState{Mechanism: status.MechanismProjectionRetry, Status: projectionRun.Status, Detail: projectionRun.ProjectionType + " run"})
	}
	if syncRun != nil {
		result = append(result, MechanismState{Mechanism: status.MechanismSyncRetry, Status: syncRun.Status, Attempts: syncRun.Attempts, LeaseUntil: syncRun.LeaseUntil, LastError: syncRun.ErrorSummary, Detail: syncRun.Mode + " sync"})
	}
	if answerJob != nil {
		result = append(result, MechanismState{Mechanism: status.MechanismAnswerLease, Status: answerJob.Status, Attempts: answerJob.Attempts, LeaseUntil: answerJob.LeaseUntil, LastError: answerJob.ErrorCode})
	}
	return result
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
	if err := pipeline.UpsertDeadLetter(i.db.WithContext(ctx), row); err != nil {
		return err
	}
	// The retry budget is spent. Release the inbox lease as FAILED so an operator
	// retry can claim it again; PROCESSING would look like a live worker.
	return pipeline.FailProcessingInbox(ctx, i.db, task.IdempotencyKey, message)
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
	if err := pipeline.RequeueInbox(i.db.WithContext(ctx), row.IdempotencyKey, now); err != nil {
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
	if err := tenantdb.InTx(ctx, i.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := cancelStages(tx, pipeline.ID, "cancelled by operator"); err != nil {
			return err
		}
		return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.PipelineRun{}).Where("id = ?", pipeline.ID).
			Updates(map[string]any{"status": StatusCancelled, "error_code": "cancelled", "error_message": "cancelled by operator", "updated_at": time.Now()}).Error
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
