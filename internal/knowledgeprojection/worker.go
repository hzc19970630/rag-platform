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
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	inboxProcessing = "PROCESSING"
	inboxSucceeded  = "SUCCEEDED"
	inboxFailed     = "FAILED"
)

// ValidateStageTransition keeps the projection state machine explicit. A retry
// may restart the failed stage, but no stage can jump forward.
func ValidateStageTransition(runStatus, stage string) error {
	switch stage {
	case StageCollect:
		if runStatus == ProjectionPending || runStatus == ProjectionRetrying || runStatus == ProjectionFailed {
			return nil
		}
	case StageGenerate:
		if runStatus == ProjectionPending || runStatus == ProjectionRunning || runStatus == ProjectionRetrying || runStatus == ProjectionFailed {
			return nil
		}
	case StageFinalize:
		if runStatus == ProjectionRunning || runStatus == ProjectionRetrying {
			return nil
		}
	}
	return fmt.Errorf("invalid wiki projection transition %s -> %s", runStatus, stage)
}

func stageRunStatus(stage string) string {
	switch stage {
	case StageCollect:
		return ProjectionPending
	default:
		return ProjectionRunning
	}
}

func nextStage(stage string) (string, bool) {
	switch stage {
	case StageCollect:
		return StageGenerate, true
	case StageGenerate:
		return StageFinalize, true
	default:
		return "", false
	}
}

// ValidateRelationStageTransition is the relation state machine: COLLECT then
// EXTRACT then FINALIZE. A retry may restart the failed stage only.
func ValidateRelationStageTransition(runStatus, stage string) error {
	switch stage {
	case StageCollect:
		if runStatus == RelationPending || runStatus == RelationRetrying || runStatus == RelationFailed {
			return nil
		}
	case StageExtract:
		if runStatus == RelationPending || runStatus == RelationExtracting || runStatus == RelationRetrying || runStatus == RelationFailed {
			return nil
		}
	case StageFinalize:
		if runStatus == RelationExtracting || runStatus == RelationRetrying {
			return nil
		}
	}
	return fmt.Errorf("invalid relation projection transition %s -> %s", runStatus, stage)
}

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

func (s *Service) processWiki(ctx context.Context, task Task) error {
	if task.SchemaVersion != SchemaWikiTask || task.ProjectionRunID == "" || task.Stage == "" || task.ProjectionType != ProjectionTypeWiki {
		return apperror.Terminalf("invalid_projection_task_envelope", "invalid wiki projection task envelope")
	}
	claimed, err := s.claimInbox(ctx, task)
	if err != nil || !claimed {
		return err
	}
	run, err := s.loadRun(ctx, task.ProjectionRunID)
	if err != nil {
		return s.finishInbox(ctx, task.IdempotencyKey, inboxFailed, err)
	}
	if run.ProjectionType != ProjectionTypeWiki {
		return s.finishInbox(ctx, task.IdempotencyKey, inboxFailed, apperror.Terminalf("projection_type_mismatch", "projection type mismatch"))
	}
	if run.Status == ProjectionReady || run.Status == ProjectionCancelled {
		return s.finishInbox(ctx, task.IdempotencyKey, inboxSucceeded, nil)
	}
	if err := ValidateStageTransition(run.Status, task.Stage); err != nil {
		return s.finishInbox(ctx, task.IdempotencyKey, inboxFailed, err)
	}
	stage, err := s.markStageProcessing(ctx, task, run)
	if err != nil {
		return err
	}
	processCtx := ctx
	if s.timeout > 0 {
		var cancel context.CancelFunc
		processCtx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	var processErr error
	switch task.Stage {
	case StageCollect:
		processErr = s.collect(processCtx, run)
	case StageGenerate:
		processErr = s.generate(processCtx, run)
	case StageFinalize:
		processErr = s.finalize(processCtx, run)
	}
	if processErr != nil {
		terminal := apperror.IsTerminal(processErr)
		if err := s.failStage(ctx, run, stage, processErr); err != nil {
			return err
		}
		if terminal {
			return s.finishInbox(ctx, task.IdempotencyKey, inboxFailed, processErr)
		}
		return processErr
	}
	return s.completeStage(ctx, task, run, stage)
}

func (s *Service) collect(ctx context.Context, run *models.ProjectionRun) error {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return apperror.Terminal("knowledge_base_missing", err)
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return apperror.Terminal("invalid_indexing_strategy", err)
	}
	if !strategy.WikiEnabled {
		return apperror.Terminalf("wiki_disabled", "wiki projection is not enabled for this knowledge base")
	}
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return apperror.Terminalf("no_evidence", "no current EvidencePackage is available for projection")
	}
	sourceHash, err := packagesSourceHash(packages, knowledgeBase.ACLVersion, s.generator.PromptVersion(), s.generator.Model())
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"source_hash": sourceHash, "acl_version": knowledgeBase.ACLVersion,
		"model": s.generator.Model(), "prompt_version": s.generator.PromptVersion(),
		"error_code": "", "error_message": "", "updated_at": time.Now(),
	}).Error
}

func (s *Service) generate(ctx context.Context, run *models.ProjectionRun) error {
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return apperror.Terminalf("no_evidence", "no current EvidencePackage is available for projection")
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return apperror.Terminal("knowledge_base_missing", err)
	}
	pages, err := s.generator.Generate(ctx, knowledgeBase.Name, packages)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return apperror.Terminalf("empty_projection", "evidence packages produced no wiki pages")
	}
	now := time.Now()
	for _, page := range pages {
		if err := s.persistPage(ctx, run, knowledgeBase, page, now); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"acl_version": knowledgeBase.ACLVersion, "model": s.generator.Model(), "prompt_version": s.generator.PromptVersion(),
		"error_code": "", "error_message": "", "updated_at": now,
	}).Error
}

func (s *Service) finalize(ctx context.Context, run *models.ProjectionRun) error {
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return err
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return apperror.Terminal("knowledge_base_missing", err)
	}
	sourceHash, err := packagesSourceHash(packages, knowledgeBase.ACLVersion, s.generator.PromptVersion(), s.generator.Model())
	if err != nil {
		return err
	}
	if run.SourceHash != "" && run.SourceHash != sourceHash {
		// Evidence changed while the run was generating. Cancel this run and
		// create its replacement in the same transaction: the package that
		// changed the hash may have been coalesced into this run, so waiting for
		// another trigger could leave the knowledge base partial forever.
		now := time.Now()
		replacement := packages[len(packages)-1]
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
				"status": ProjectionCancelled, "error_code": "source_changed",
				"error_message": "evidence changed during projection; replacement run created",
				"completed_at":  &now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			return s.insertRun(tx, knowledgeBase, replacement.ID, sourceHash, now)
		})
	}
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": ProjectionReady, "source_hash": sourceHash, "acl_version": knowledgeBase.ACLVersion,
		"completed_at": &now, "error_code": "", "error_message": "", "updated_at": now,
	}).Error
}

func (s *Service) persistPage(ctx context.Context, run *models.ProjectionRun, knowledgeBase models.KnowledgeBase, page GeneratedPage, now time.Time) error {
	if len(page.Blocks) == 0 {
		return nil
	}
	for _, block := range page.Blocks {
		if len(block.SourceChunkIDs) == 0 || strings.TrimSpace(block.Content) == "" {
			return apperror.Terminalf("untraceable_block", "page %s has a block without source chunks", page.Title)
		}
	}
	blocksJSON, err := json.Marshal(page.Blocks)
	if err != nil {
		return fmt.Errorf("encode wiki blocks: %w", err)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.WikiPage
		lookupErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("knowledge_base_id = ? AND page_type = ? AND slug = ?", knowledgeBase.ID, page.PageType, page.Slug).
			First(&existing).Error
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		if lookupErr == nil && existing.SourceHash == page.SourceHash && existing.Status != PageStale && existing.CurrentVersionID != nil {
			return nil
		}
		pageRow := existing
		created := errors.Is(lookupErr, gorm.ErrRecordNotFound)
		if created {
			pageRow = models.WikiPage{
				ID: stableID("wp", knowledgeBase.ID, page.PageType, page.Slug), TenantID: knowledgeBase.TenantID,
				KnowledgeBaseID: knowledgeBase.ID, PageType: page.PageType, Slug: page.Slug, Title: page.Title,
				Status: PageDraft, ACLVersion: knowledgeBase.ACLVersion, SourceHash: page.SourceHash,
				CreatedAt: now, UpdatedAt: now,
			}
			if page.DocumentID != "" {
				documentID := page.DocumentID
				pageRow.DocumentID = &documentID
			}
			if err := tx.Create(&pageRow).Error; err != nil {
				return err
			}
		}
		var nextVersion int
		if err := tx.Model(&models.WikiPageVersion{}).Where("wiki_page_id = ?", pageRow.ID).
			Select("COALESCE(MAX(version), 0)").Scan(&nextVersion).Error; err != nil {
			return err
		}
		nextVersion++
		version := models.WikiPageVersion{
			ID: stableID("wpv", pageRow.ID, fmt.Sprint(nextVersion), page.SourceHash), WikiPageID: pageRow.ID,
			TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID, Version: nextVersion,
			Content: contentFromBlocks(page.Blocks), Blocks: string(blocksJSON), SourceHash: page.SourceHash,
			GenerationModel: s.generator.Model(), PromptVersion: s.generator.PromptVersion(),
			ReviewStatus: ReviewPending, ACLVersion: knowledgeBase.ACLVersion, CreatedBy: "system", CreatedAt: now,
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		sources := make([]models.WikiPageSource, 0, len(page.Sources))
		for _, source := range page.Sources {
			source.WikiPageVersionID = version.ID
			source.TenantID = knowledgeBase.TenantID
			source.KnowledgeBaseID = knowledgeBase.ID
			sources = append(sources, source)
		}
		if len(sources) > 0 {
			if err := tx.Create(&sources).Error; err != nil {
				return err
			}
		}
		status := PageDraft
		if pageRow.PublishedVersionID != nil {
			status = PageReviewRequired
		}
		return tx.Model(&models.WikiPage{}).Where("id = ?", pageRow.ID).Updates(map[string]any{
			"current_version_id": version.ID, "source_hash": page.SourceHash, "acl_version": knowledgeBase.ACLVersion,
			"title": page.Title, "status": status, "updated_at": now,
		}).Error
	})
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

func contentFromBlocks(blocks []models.WikiBlock) string {
	var builder strings.Builder
	for index, block := range blocks {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		if heading := strings.TrimSpace(block.HeadingPath); heading != "" {
			builder.WriteString("## ")
			builder.WriteString(heading)
			builder.WriteString("\n\n")
		}
		builder.WriteString(strings.TrimSpace(block.Content))
	}
	return builder.String()
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
