package knowledgeprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/pipeline"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	if spec, ok := wikiStageTable.Spec(stage); ok {
		return spec.Processing
	}
	return ProjectionRunning
}

func nextStage(stage string) (string, bool) {
	next, ok := wikiStageTable.Next(stage)
	return next.Stage, ok
}

func (s *Service) processWiki(ctx context.Context, task Task) error {
	if task.SchemaVersion != SchemaWikiTask || task.ProjectionRunID == "" || task.Stage == "" || task.ProjectionType != ProjectionTypeWiki {
		return apperror.Terminalf("invalid_projection_task_envelope", "invalid wiki projection task envelope")
	}
	claimed, err := s.claimInbox(ctx, task)
	if err != nil || !claimed {
		return err
	}
	processCtx, releaseLease, retainLease, leaseLost := s.runtime().Heartbeat(ctx, task.IdempotencyKey)
	retainOnReturn := false
	defer func() {
		if retainOnReturn {
			retainLease()
			return
		}
		releaseLease()
	}()
	run, err := s.loadRun(ctx, task.TenantID, task.ProjectionRunID)
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
	if err := s.runtime().Start(ctx, projectionStage{task: task, run: run}); err != nil {
		return err
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		processCtx, cancel = context.WithTimeout(processCtx, s.timeout)
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
	if leaseLost() {
		return fmt.Errorf("inbox lease lost for %s: %w", task.IdempotencyKey, context.Canceled)
	}
	if processErr != nil {
		terminal := apperror.IsTerminal(processErr)
		if err := s.failStage(ctx, task, run, processErr); err != nil {
			return err
		}
		if terminal {
			return s.finishInbox(ctx, task.IdempotencyKey, inboxFailed, processErr)
		}
		if err := pipeline.RetainFailedInbox(ctx, s.db, task.IdempotencyKey, processErr.Error()); err != nil {
			return err
		}
		retainOnReturn = true
		return processErr
	}
	return s.runtime().Complete(ctx, task.IdempotencyKey, projectionStage{task: task, run: run})
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
	pages, stats, err := s.generator.Generate(ctx, knowledgeBase.Name, packages)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return apperror.Terminalf("empty_projection", "evidence packages produced no wiki pages")
	}
	s.recordGeneration(ProjectionTypeWiki, stats.Generated, stats.Reused)
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
		if err := tenantdb.MustScoped(tx, knowledgeBase.TenantID).Model(&models.WikiPageVersion{}).Where("wiki_page_id = ?", pageRow.ID).
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
