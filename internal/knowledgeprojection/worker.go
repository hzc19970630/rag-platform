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
	"github.com/example/rag-platform/internal/pipeline"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	inboxProcessing = "PROCESSING"
	inboxSucceeded  = "SUCCEEDED"
	inboxFailed     = "FAILED"
)

var wikiStageTable = pipeline.NewStageTable(
	pipeline.StageSpec{Stage: StageCollect, Processing: ProjectionPending, Completed: ProjectionRunning, EventType: "projection.wiki"},
	pipeline.StageSpec{Stage: StageGenerate, Processing: ProjectionRunning, Completed: ProjectionRunning, EventType: "projection.wiki"},
	pipeline.StageSpec{Stage: StageFinalize, Processing: ProjectionRunning, Completed: ProjectionReady},
)

var relationStageTable = pipeline.NewStageTable(
	pipeline.StageSpec{Stage: StageCollect, Processing: RelationPending, Completed: RelationExtracting, EventType: "projection.relation"},
	pipeline.StageSpec{Stage: StageExtract, Processing: RelationExtracting, Completed: RelationExtracting, EventType: "projection.relation"},
	pipeline.StageSpec{Stage: StageFinalize, Processing: RelationExtracting, Completed: RelationReady},
)

func projectionStages(projectionType string) *pipeline.StageTable {
	if projectionType == ProjectionTypeRelation {
		return relationStageTable
	}
	return wikiStageTable
}

func stageStatusFor(projectionType, stage string) string {
	if spec, ok := projectionStages(projectionType).Spec(stage); ok {
		return spec.Processing
	}
	if projectionType == ProjectionTypeRelation {
		return RelationExtracting
	}
	return ProjectionRunning
}

func nextStageFor(projectionType, stage string) (string, bool) {
	next, ok := projectionStages(projectionType).Next(stage)
	return next.Stage, ok
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

// projectionStage adapts one projection stage to the shared pipeline runtime.
// Wiki and relation keep their own stages, status names and transition rules;
// claim, lease, inbox outcome and outbox reopening are owned by pipeline.
type projectionStage struct {
	task Task
	run  *models.ProjectionRun
}

func (p projectionStage) Start(tx *gorm.DB, now time.Time) error {
	var stage models.ProjectionStageRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stage, "idempotency_key = ?", p.task.IdempotencyKey).Error; err != nil {
		return err
	}
	if stage.ProjectionRunID != p.run.ID || stage.Stage != p.task.Stage || stage.ProjectionType != p.run.ProjectionType {
		return fmt.Errorf("projection stage does not belong to run")
	}
	if err := tenantdb.MustScoped(tx, p.run.TenantID).Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", p.run.ID, ProjectionCancelled).Updates(map[string]any{
		"status": stageStatusFor(p.run.ProjectionType, p.task.Stage), "error_code": "", "error_message": "", "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&models.ProjectionStageRun{}).Where("id = ?", stage.ID).Updates(map[string]any{
		"status": StageProcessing, "attempts": gorm.Expr("attempts + 1"), "started_at": &now,
		"error_code": "", "error_message": "", "updated_at": now,
	}).Error
}

func (p projectionStage) Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error) {
	if err := tx.Model(&models.ProjectionStageRun{}).Where("idempotency_key = ?", p.task.IdempotencyKey).Updates(map[string]any{
		"status": StageSucceeded, "completed_at": &now, "error_code": "", "error_message": "", "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	next, hasNext := nextStageFor(p.run.ProjectionType, p.task.Stage)
	if !hasNext {
		return nil, nil
	}
	nextKey := p.run.ID + ":" + next
	var nextStageRow models.ProjectionStageRun
	if err := tx.Where("idempotency_key = ?", nextKey).First(&nextStageRow).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		nextStageRow = models.ProjectionStageRun{
			ProjectionRunID: p.run.ID, ProjectionType: p.run.ProjectionType, Stage: next,
			Status: StagePending, IdempotencyKey: nextKey,
		}
		if err := tx.Create(&nextStageRow).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	event, err := stageEvent(p.run, next, nextStageRow.Attempts)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (p projectionStage) Fail(tx *gorm.DB, code, message string, now time.Time) error {
	if err := tx.Model(&models.ProjectionStageRun{}).Where("idempotency_key = ?", p.task.IdempotencyKey).Updates(map[string]any{
		"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return tenantdb.MustScoped(tx, p.run.TenantID).Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", p.run.ID, ProjectionCancelled).Updates(map[string]any{
		"status": failedStatusFor(p.run.ProjectionType), "error_code": code, "error_message": message, "updated_at": now,
	}).Error
}

func stageEvent(run *models.ProjectionRun, stage string, attempt int) (models.OutboxEvent, error) {
	key := run.ID + ":" + stage
	schema, eventType := SchemaWikiTask, "projection.wiki"
	if run.ProjectionType == ProjectionTypeRelation {
		schema, eventType = SchemaRelationTask, "projection.relation"
	}
	if spec, ok := projectionStages(run.ProjectionType).Spec(stage); ok && spec.EventType != "" {
		eventType = spec.EventType
	}
	task := Task{
		SchemaVersion: schema, TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID,
		EvidencePackageID: run.EvidencePackageID, ProjectionRunID: run.ID,
		ProjectionType: run.ProjectionType, Stage: stage, IdempotencyKey: key, Attempt: attempt,
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return models.OutboxEvent{}, err
	}
	return models.OutboxEvent{
		TenantID: run.TenantID, AggregateType: "projection_run", AggregateID: run.ID,
		EventType: eventType, IdempotencyKey: key, Payload: string(payload),
	}, nil
}

// reopenStage is the projection call site of the single outbox reopen
// implementation. A retry re-opens the same durable event; the task attempt
// changes the queue id so Asynq's retained completed record cannot suppress it.
func reopenStage(tx *gorm.DB, run *models.ProjectionRun, stage string, attempt int) error {
	event, err := stageEvent(run, stage, attempt)
	if err != nil {
		return err
	}
	return pipeline.ReopenEvent(tx, event)
}

func (s *Service) claimInbox(ctx context.Context, task Task) (bool, error) {
	taskType := "projection." + task.ProjectionType + "." + task.Stage
	return s.runtime().Claim(ctx, task.IdempotencyKey, taskType, 0)
}

func (s *Service) failStage(ctx context.Context, task Task, run *models.ProjectionRun, processErr error) error {
	code, message := splitError(processErr)
	return s.runtime().Fail(ctx, projectionStage{task: task, run: run}, code, message)
}

func (s *Service) finishInbox(ctx context.Context, key, status string, processErr error) error {
	return s.runtime().FinishInbox(ctx, key, status, processErr)
}

func (s *Service) loadRun(ctx context.Context, tenantID, id string) (*models.ProjectionRun, error) {
	var run models.ProjectionRun
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).First(&run, "id = ?", id).Error; err != nil {
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
