package knowledgeprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	inboxProcessing = pipeline.InboxProcessing
	inboxSucceeded  = pipeline.InboxSucceeded
	inboxFailed     = pipeline.InboxFailed
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

// projectionStage lives in relation_worker.go: it calls the shared pipeline
// runtime for both wiki and relation stages and fans relation extraction out
// into unit events.

func stageEvent(run *models.ProjectionRun, stage string, attempt int, traceParent string) (models.OutboxEvent, error) {
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
		TraceParent: traceParent,
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
func reopenStage(ctx context.Context, tx *gorm.DB, run *models.ProjectionRun, stage string, attempt int) error {
	event, err := stageEvent(run, stage, attempt, observability.InjectTraceParent(ctx))
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
