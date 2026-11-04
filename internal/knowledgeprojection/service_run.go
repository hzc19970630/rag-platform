package knowledgeprojection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
)

// TriggerWiki is called by Pipeline A after it publishes an EvidencePackage.
// It coalesces triggers per knowledge base: a run projects every current
// package, so one live run is enough and parallel runs cannot race on pages.
func (s *Service) TriggerWiki(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string, aclVersion int64) error {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return err
	}
	if !strategy.WikiEnabled {
		return nil
	}
	packages, err := s.loadCurrentPackages(ctx, tenantID, knowledgeBaseID)
	if err != nil || len(packages) == 0 {
		return err
	}
	sourceHash, _ := packagesSourceHash(packages, knowledgeBase.ACLVersion, s.generator.PromptVersion(), s.generator.Model())
	var latest models.ProjectionRun
	lookupErr := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeWiki).
		Order("created_at DESC, id DESC").First(&latest).Error
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return lookupErr
	}
	if lookupErr == nil {
		switch latest.Status {
		case ProjectionPending, ProjectionRunning, ProjectionRetrying:
			return nil
		case ProjectionReady:
			if latest.SourceHash == sourceHash && latest.PromptVersion == s.generator.PromptVersion() && latest.Model == s.generator.Model() {
				return nil
			}
		case ProjectionFailed:
			return s.resetRunForRetry(ctx, &latest)
		}
	}
	_ = aclVersion
	return s.createRun(ctx, knowledgeBase, evidencePackageID, sourceHash)
}

// RebuildKnowledgeBase is the internal lifecycle hook used after wiki is
// enabled on a knowledge base. It always creates a fresh run so existing pages
// are regenerated against the current evidence and prompt version.
func (s *Service) RebuildKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string) (int, error) {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return 0, err
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return 0, err
	}
	if !strategy.WikiEnabled {
		return 0, fmt.Errorf("wiki projection is not enabled")
	}
	packages, err := s.loadCurrentPackages(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return 0, err
	}
	if len(packages) == 0 {
		return 0, nil
	}
	var active models.ProjectionRun
	lookupErr := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ? AND status IN ?", knowledgeBaseID, ProjectionTypeWiki,
		[]string{ProjectionPending, ProjectionRunning, ProjectionRetrying}).First(&active).Error
	if lookupErr == nil {
		return 1, nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return 0, lookupErr
	}
	sourceHash, _ := packagesSourceHash(packages, knowledgeBase.ACLVersion, s.generator.PromptVersion(), s.generator.Model())
	last := packages[len(packages)-1]
	if err := s.createRun(ctx, knowledgeBase, last.ID, sourceHash); err != nil {
		return 0, err
	}
	return 1, nil
}

// RebuildRelations regenerates the graph snapshot from current evidence. The
// previous ACTIVE snapshot stays queryable until the new one is reviewed and
// activated; rebuilding is not a silent publish.
func (s *Service) RebuildRelations(ctx context.Context, tenantID, knowledgeBaseID string) (int, error) {
	if s.relation == nil {
		return 0, fmt.Errorf("relation projection is not configured")
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return 0, err
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return 0, err
	}
	if !strategy.GraphEnabled {
		return 0, fmt.Errorf("relation projection is not enabled")
	}
	packages, err := s.loadCurrentPackages(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return 0, err
	}
	if len(packages) == 0 {
		return 0, nil
	}
	var active models.ProjectionRun
	lookupErr := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ? AND status IN ?", knowledgeBaseID, ProjectionTypeRelation,
		activeStatusesFor(ProjectionTypeRelation)).First(&active).Error
	if lookupErr == nil {
		return 1, nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return 0, lookupErr
	}
	last := packages[len(packages)-1]
	if err := s.createRunFor(ctx, knowledgeBase, ProjectionTypeRelation, last.ID, s.relationSourceHash(packages, knowledgeBase.ACLVersion)); err != nil {
		return 0, err
	}
	return 1, nil
}

// TriggerRelation is the Pipeline B relation entry point. It is independent of
// TriggerWiki so a wiki failure cannot suppress graph extraction and vice
// versa; both read the same immutable EvidencePackages.
func (s *Service) TriggerRelation(ctx context.Context, tenantID, knowledgeBaseID, evidencePackageID string, aclVersion int64) error {
	if s.relation == nil {
		return nil
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return err
	}
	if !strategy.GraphEnabled {
		return nil
	}
	packages, err := s.loadCurrentPackages(ctx, tenantID, knowledgeBaseID)
	if err != nil || len(packages) == 0 {
		return err
	}
	sourceHash := s.relationSourceHash(packages, knowledgeBase.ACLVersion)
	var latest models.ProjectionRun
	lookupErr := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeRelation).
		Order("created_at DESC, id DESC").First(&latest).Error
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return lookupErr
	}
	if lookupErr == nil {
		switch latest.Status {
		case RelationPending, RelationExtracting, RelationRetrying:
			return nil
		case RelationReady:
			if latest.SourceHash == sourceHash && latest.PromptVersion == s.relation.PromptVersion() && latest.Model == s.relation.Model() {
				return nil
			}
		case RelationFailed:
			return s.resetRunForRetry(ctx, &latest)
		}
	}
	_ = aclVersion
	return s.createRunFor(ctx, knowledgeBase, ProjectionTypeRelation, evidencePackageID, sourceHash)
}

func (s *Service) createRun(ctx context.Context, knowledgeBase models.KnowledgeBase, evidencePackageID, sourceHash string) error {
	return s.createRunFor(ctx, knowledgeBase, ProjectionTypeWiki, evidencePackageID, sourceHash)
}

func (s *Service) createRunFor(ctx context.Context, knowledgeBase models.KnowledgeBase, projectionType, evidencePackageID, sourceHash string) error {
	now := time.Now()
	activeStatuses := activeStatusesFor(projectionType)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.insertRunFor(tx, knowledgeBase, projectionType, evidencePackageID, sourceHash, now)
	})
	if err != nil {
		// A concurrent trigger may have won the one-active-run index. That is the
		// intended coalescing outcome, not a request failure.
		var active models.ProjectionRun
		if lookupErr := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ? AND status IN ?", knowledgeBase.ID, projectionType,
			activeStatuses).First(&active).Error; lookupErr == nil {
			return nil
		}
		return fmt.Errorf("create %s projection run: %w", projectionType, err)
	}
	return nil
}

func activeStatusesFor(projectionType string) []string {
	if projectionType == ProjectionTypeRelation {
		return []string{RelationPending, RelationExtracting, RelationRetrying}
	}
	return []string{ProjectionPending, ProjectionRunning, ProjectionRetrying}
}

// insertRun creates one run, its first stage row and the durable outbox event.
// It takes a transaction so the caller can cancel a superseded run and create
// its replacement atomically.
func (s *Service) insertRun(tx *gorm.DB, knowledgeBase models.KnowledgeBase, evidencePackageID, sourceHash string, now time.Time) error {
	return s.insertRunFor(tx, knowledgeBase, ProjectionTypeWiki, evidencePackageID, sourceHash, now)
}

func (s *Service) insertRunFor(tx *gorm.DB, knowledgeBase models.KnowledgeBase, projectionType, evidencePackageID, sourceHash string, now time.Time) error {
	runID := stableID("prj", knowledgeBase.ID, projectionType, evidencePackageID, sourceHash, fmt.Sprint(now.UnixNano()))
	run := models.ProjectionRun{
		ID: runID, TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID,
		EvidencePackageID: evidencePackageID, ProjectionType: projectionType,
		Status: ProjectionPending, SourceHash: sourceHash, ACLVersion: knowledgeBase.ACLVersion,
		CreatedAt: now, UpdatedAt: now,
	}
	if projectionType == ProjectionTypeRelation {
		if s.relation == nil {
			return fmt.Errorf("relation projection is not configured")
		}
		run.Status = RelationPending
		run.Model = s.relation.Model()
		run.PromptVersion = s.relation.PromptVersion()
	} else {
		run.Model = s.generator.Model()
		run.PromptVersion = s.generator.PromptVersion()
	}
	if err := tx.Create(&run).Error; err != nil {
		return err
	}
	stage := models.ProjectionStageRun{
		ProjectionRunID: run.ID, ProjectionType: projectionType, Stage: StageCollect,
		Status: StagePending, IdempotencyKey: run.ID + ":" + StageCollect, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&stage).Error; err != nil {
		return err
	}
	return enqueueStage(tx, &run, StageCollect, 0)
}

func (s *Service) resetRunForRetry(ctx context.Context, run *models.ProjectionRun) error {
	now := time.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stages []models.ProjectionStageRun
		if err := tx.Where("projection_run_id = ?", run.ID).Order("id").Find(&stages).Error; err != nil {
			return err
		}
		if len(stages) == 0 {
			return apperror.ErrConflict
		}
		retryStage := stages[0]
		for _, stage := range stages {
			if stage.Status != StageSucceeded {
				retryStage = stage
				break
			}
		}
		if err := tx.Model(&models.ProjectionStageRun{}).Where("projection_run_id = ? AND status <> ?", run.ID, StageSucceeded).Updates(map[string]any{
			"status": StagePending, "error_code": "", "error_message": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status": retryStatusFor(run.ProjectionType), "error_code": "", "error_message": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		// An independent retry reopens the inbox as well: the original attempt may
		// have succeeded before the failure was recorded, and Asynq keeps the
		// completed task record. The next attempt number gives the enqueue a fresh
		// task id, and a non-SUCCEEDED inbox lets the worker claim it again.
		if err := tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", run.ID+":"+retryStage.Stage).Updates(map[string]any{
			"status": inboxFailed, "processed_at": nil, "last_error": "", "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := enqueueStage(tx, run, retryStage.Stage, retryStage.Attempts+1); err != nil {
			return err
		}
		run.Status = retryStatusFor(run.ProjectionType)
		return nil
	})
}

// InvalidateKnowledgeBase is called on ACL/version changes. Removing the
// Knowledge Index rows makes the derived page disappear immediately, before a
// rebuild has run.
func (s *Service) InvalidateKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID, reason string) error {
	now := time.Now()
	// Graph invalidation runs before the wiki transaction: if it fails, the
	// caller still sees an error and no derived state is left queryable under
	// the old ACL. The two stores cannot share a transaction.
	if s.store != nil {
		if err := s.store.MarkStaleByKnowledgeBase(ctx, tenantID, knowledgeBaseID, now); err != nil {
			return fmt.Errorf("invalidate graph snapshots: %w", err)
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.WikiPage{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND status <> ? AND status <> ?", tenantID, knowledgeBaseID, PageArchived, PageStale).
			Updates(map[string]any{"status": PageStale, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).Delete(&models.KnowledgeIndexRecord{}).Error; err != nil {
			return err
		}
		message := strings.TrimSpace(reason)
		if message == "" {
			message = "knowledge_base_changed"
		}
		return tx.Model(&models.ProjectionRun{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND status IN ?", tenantID, knowledgeBaseID,
				append(activeStatusesFor(ProjectionTypeWiki), activeStatusesFor(ProjectionTypeRelation)...)).
			Updates(map[string]any{"status": ProjectionCancelled, "error_code": "invalidated", "error_message": message, "updated_at": now}).Error
	})
}

// ScheduleMissing is the self-healing sweep. A trigger that failed before it
// created a run leaves no durable projection intent, so maintenance periodically
// compares the current evidence hash with the latest run and starts one when
// wiki is enabled. Failed runs are deliberately left to the operator retry path.
func (s *Service) ScheduleMissing(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	var knowledgeBases []models.KnowledgeBase
	if err := s.db.WithContext(ctx).
		Where("indexing_strategy->>'wiki_enabled' = 'true' OR indexing_strategy->>'graph_enabled' = 'true'").
		Order("updated_at").Limit(limit).Find(&knowledgeBases).Error; err != nil {
		return 0, fmt.Errorf("list projection-enabled knowledge bases: %w", err)
	}
	started := 0
	for _, knowledgeBase := range knowledgeBases {
		strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
		if err != nil {
			continue
		}
		packages, err := s.loadCurrentPackages(ctx, knowledgeBase.TenantID, knowledgeBase.ID)
		if err != nil || len(packages) == 0 {
			continue
		}
		if strategy.WikiEnabled {
			created, err := s.scheduleProjection(ctx, knowledgeBase, packages, ProjectionTypeWiki, s.generator.PromptVersion(), s.generator.Model())
			if err != nil {
				return started, err
			}
			if created {
				started++
			}
		}
		if strategy.GraphEnabled && s.relation != nil {
			created, err := s.scheduleProjection(ctx, knowledgeBase, packages, ProjectionTypeRelation, s.relation.PromptVersion(), s.relation.Model())
			if err != nil {
				return started, err
			}
			if created {
				started++
			}
		}
	}
	return started, nil
}

func (s *Service) scheduleProjection(ctx context.Context, knowledgeBase models.KnowledgeBase, packages []ingestion.EvidencePackage, projectionType, promptVersion, model string) (bool, error) {
	sourceHash, err := packagesSourceHash(packages, knowledgeBase.ACLVersion, promptVersion, model)
	if err != nil {
		return false, nil
	}
	var latest models.ProjectionRun
	lookupErr := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBase.ID, projectionType).
		Order("created_at DESC, id DESC").First(&latest).Error
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if lookupErr == nil {
		for _, status := range activeStatusesFor(projectionType) {
			if latest.Status == status {
				return false, nil
			}
		}
		if latest.Status == failedStatusFor(projectionType) {
			return false, nil
		}
		if latest.Status == readyStatusFor(projectionType) &&
			latest.SourceHash == sourceHash && latest.PromptVersion == promptVersion && latest.Model == model &&
			latest.ACLVersion == knowledgeBase.ACLVersion {
			return false, nil
		}
	}
	last := packages[len(packages)-1]
	if err := s.createRunFor(ctx, knowledgeBase, projectionType, last.ID, sourceHash); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) Status(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (ProjectionStatus, error) {
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return ProjectionStatus{}, err
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, principal.TenantID, knowledgeBaseID)
	if err != nil {
		return ProjectionStatus{}, apperror.ErrNotFound
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return ProjectionStatus{}, err
	}
	var runs []models.ProjectionRun
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeWiki).
		Order("created_at DESC, id DESC").Limit(20).Find(&runs).Error; err != nil {
		return ProjectionStatus{}, err
	}
	var relationRuns []models.ProjectionRun
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeRelation).
		Order("created_at DESC, id DESC").Limit(20).Find(&relationRuns).Error; err != nil {
		return ProjectionStatus{}, err
	}
	counts, err := s.pageCounts(ctx, principal.TenantID, knowledgeBaseID)
	if err != nil {
		return ProjectionStatus{}, err
	}
	graph, err := s.graphCounts(ctx, principal.TenantID, knowledgeBaseID)
	if err != nil {
		return ProjectionStatus{}, err
	}
	joinStatus, err := s.joinStatus(ctx, knowledgeBase, strategy)
	if err != nil {
		return ProjectionStatus{}, err
	}
	return ProjectionStatus{
		KnowledgeBaseID: knowledgeBaseID, JoinStatus: joinStatus,
		WikiEnabled: strategy.WikiEnabled, GraphEnabled: strategy.GraphEnabled,
		Runs: runs, RelationRuns: relationRuns, Pages: counts, Graph: graph,
	}, nil
}

func (s *Service) ListRuns(ctx context.Context, principal authz.Principal, knowledgeBaseID string, limit int) ([]models.ProjectionRun, error) {
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var runs []models.ProjectionRun
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, ProjectionTypeWiki).
		Order("created_at DESC, id DESC").Limit(limit).Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Service) GetRun(ctx context.Context, principal authz.Principal, runID string) (RunDetail, error) {
	var run models.ProjectionRun
	if err := s.db.WithContext(ctx).First(&run, "id = ? AND tenant_id = ?", runID, principal.TenantID).Error; err != nil {
		return RunDetail{}, apperror.ErrNotFound
	}
	if _, err := s.authorize(ctx, principal, run.KnowledgeBaseID, "read"); err != nil {
		return RunDetail{}, err
	}
	var stages []models.ProjectionStageRun
	if err := s.db.WithContext(ctx).Where("projection_run_id = ?", run.ID).Order("id").Find(&stages).Error; err != nil {
		return RunDetail{}, err
	}
	return RunDetail{Run: run, Stages: stages}, nil
}

func (s *Service) Retry(ctx context.Context, principal authz.Principal, runID string) (models.ProjectionRun, error) {
	if principal.CredentialType == "api_key" {
		return models.ProjectionRun{}, apperror.ErrForbidden
	}
	var run models.ProjectionRun
	if err := s.db.WithContext(ctx).First(&run, "id = ? AND tenant_id = ?", runID, principal.TenantID).Error; err != nil {
		return models.ProjectionRun{}, apperror.ErrNotFound
	}
	if _, err := s.authorize(ctx, principal, run.KnowledgeBaseID, "write"); err != nil {
		return models.ProjectionRun{}, err
	}
	if run.Status != failedStatusFor(run.ProjectionType) {
		return models.ProjectionRun{}, fmt.Errorf("%w: only failed projection runs can be retried", apperror.ErrConflict)
	}
	if err := s.resetRunForRetry(ctx, &run); err != nil {
		return models.ProjectionRun{}, err
	}
	action := "wiki_retry"
	resourceType := "wiki_page"
	if run.ProjectionType == ProjectionTypeRelation {
		action = "graph_retry"
		resourceType = "graph_run"
	}
	s.recordAuditResource(ctx, principal, run.KnowledgeBaseID, resourceType, run.ID, action)
	return run, nil
}

// Reconcile is the worker maintenance sweep. It stales pages and graph
// snapshots whose sources or ACL are no longer current. It never touches
// Pipeline A.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	wikiCount, err := s.reconcileWiki(ctx)
	if err != nil {
		return wikiCount, err
	}
	graphCount, err := s.reconcileGraph(ctx)
	return wikiCount + graphCount, err
}

func (s *Service) reconcileWiki(ctx context.Context) (int, error) {
	now := time.Now()
	var staleIDs []string
	if err := s.db.WithContext(ctx).Table("wiki_pages p").
		Select("p.id").
		Where("p.status IN ?", []string{PagePublished, PageReviewRequired, PageDraft}).
		Where(`p.acl_version <> (SELECT kb.acl_version FROM knowledge_bases kb WHERE kb.id = p.knowledge_base_id)
		    OR EXISTS (
		        SELECT 1 FROM wiki_page_versions v
		        JOIN wiki_page_sources s ON s.wiki_page_version_id = v.id
		        LEFT JOIN documents d ON d.id = s.document_id
		        LEFT JOIN document_versions dv ON dv.id = s.document_version_id
		        WHERE v.id = COALESCE(p.published_version_id, p.current_version_id)
		          AND (d.id IS NULL OR d.deleted_at IS NOT NULL OR d.latest_version_id <> s.document_version_id
		               OR dv.status <> ?)
		    )`, status.EvidenceReady).Scan(&staleIDs).Error; err != nil {
		return 0, err
	}
	if len(staleIDs) == 0 {
		return 0, nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.WikiPage{}).Where("id IN ?", staleIDs).Updates(map[string]any{"status": PageStale, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Where("wiki_page_id IN ?", staleIDs).Delete(&models.KnowledgeIndexRecord{}).Error
	})
	return len(staleIDs), err
}

// reconcileGraph enforces the same invariants at snapshot level: a pending or
// active snapshot whose ACL or any relation source drifted becomes STALE and
// therefore stops answering queries.
func (s *Service) reconcileGraph(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	staleIDs, err := s.store.StaleSnapshotIDs(ctx)
	if err != nil {
		return 0, err
	}
	if len(staleIDs) == 0 {
		return 0, nil
	}
	if err := s.store.MarkStale(ctx, staleIDs, time.Now()); err != nil {
		return 0, err
	}
	return len(staleIDs), nil
}
