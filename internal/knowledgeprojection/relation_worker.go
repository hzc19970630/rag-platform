package knowledgeprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection/relation"
	"github.com/hzc19970630/rag-platform/internal/pipeline"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/inboxlease"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/relationstore"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errRelationEvidenceChanged marks a unit whose package is no longer the
// current evidence. The run is cancelled and replaced instead of reporting a
// unit failure.
var errRelationEvidenceChanged = errors.New("relation evidence changed")

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

// validateRelationUnitTransition allows a unit to run while the run is pending,
// extracting, or waiting for a retry. A failed run accepts unit retries; a
// cancelled or ready run is acknowledged before this check.
func validateRelationUnitTransition(runStatus string) error {
	switch runStatus {
	case RelationPending, RelationExtracting, RelationRetrying, RelationFailed:
		return nil
	}
	return fmt.Errorf("invalid relation unit transition %s -> %s", runStatus, StageExtract)
}

func (s *Service) processRelation(ctx context.Context, task Task) error {
	if task.SchemaVersion != SchemaRelationTask || task.ProjectionRunID == "" || task.Stage == "" || task.ProjectionType != ProjectionTypeRelation {
		return apperror.Terminalf("invalid_relation_projection_task_envelope", "invalid relation projection task envelope")
	}
	if task.UnitID != "" {
		if task.Stage != StageExtract {
			return apperror.Terminalf("invalid_relation_unit_task", "only EXTRACT tasks may carry a unit id")
		}
		return s.processRelationUnit(ctx, task)
	}
	if s.relation == nil {
		return apperror.Terminalf("relation_extractor_missing", "relation projection is not configured")
	}
	claimed, err := s.claimInbox(ctx, task)
	if errors.Is(err, inboxlease.ErrStaleDelivery) {
		slog.Info("stale_delivery_acked", "idempotency_key", task.IdempotencyKey)
		return nil
	}
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
	if run.ProjectionType != ProjectionTypeRelation {
		return s.finishInbox(ctx, task.IdempotencyKey, inboxFailed, apperror.Terminalf("projection_type_mismatch", "projection type mismatch"))
	}
	if run.Status == RelationReady || run.Status == ProjectionCancelled {
		return s.finishInbox(ctx, task.IdempotencyKey, inboxSucceeded, nil)
	}
	if err := ValidateRelationStageTransition(run.Status, task.Stage); err != nil {
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
	var packages []ingestion.EvidencePackage
	switch task.Stage {
	case StageCollect:
		packages, processErr = s.collectRelation(processCtx, run)
	case StageExtract:
		// A task enqueued before units existed only fans out the current packages
		// into units; the stage row stays open until the last unit completes.
		packages, processErr = s.loadCurrentPackages(processCtx, run.TenantID, run.KnowledgeBaseID)
		if processErr == nil && len(packages) == 0 {
			processErr = apperror.Terminalf("no_evidence", "no current EvidencePackage is available for projection")
		}
	case StageFinalize:
		processErr = s.finalizeRelation(processCtx, run)
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
	return s.runtime().Complete(ctx, task.IdempotencyKey, projectionStage{task: task, run: run, packages: packages, unitChunks: s.unitChunks})
}

func (s *Service) collectRelation(ctx context.Context, run *models.ProjectionRun) ([]ingestion.EvidencePackage, error) {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return nil, apperror.Terminal("knowledge_base_missing", err)
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return nil, apperror.Terminal("invalid_indexing_strategy", err)
	}
	if !strategy.GraphEnabled {
		return nil, apperror.Terminalf("graph_disabled", "relation projection is not enabled for this knowledge base")
	}
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	if len(packages) == 0 {
		return nil, apperror.Terminalf("no_evidence", "no current EvidencePackage is available for projection")
	}
	sourceHash := s.relationSourceHash(packages, knowledgeBase.ACLVersion)
	err = s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"source_hash": sourceHash, "acl_version": knowledgeBase.ACLVersion,
		"model": s.relation.Model(), "prompt_version": s.relation.PromptVersion(),
		"error_code": "", "error_message": "", "updated_at": time.Now(),
	}).Error
	if err != nil {
		return nil, err
	}
	run.SourceHash = sourceHash
	return packages, nil
}

func (s *Service) relationSourceHash(packages []ingestion.EvidencePackage, aclVersion int64) string {
	hash, _ := packagesSourceHash(packages, aclVersion, s.relation.PromptVersion(), s.relation.Model())
	return hash
}

// unitEvent is the durable queue event for one extraction unit. The unit id is
// both the inbox key and the outbox idempotency key, so a retry rewrites one
// event instead of creating duplicates.
func unitEvent(run *models.ProjectionRun, unit models.ProjectionUnit, attempt int) (models.OutboxEvent, error) {
	task := Task{
		SchemaVersion: SchemaRelationTask, TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID,
		EvidencePackageID: unit.PackageID, ProjectionRunID: run.ID,
		ProjectionType: ProjectionTypeRelation, Stage: StageExtract,
		UnitID: unit.ID, IdempotencyKey: unit.ID, Attempt: attempt,
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return models.OutboxEvent{}, err
	}
	return models.OutboxEvent{
		TenantID: run.TenantID, AggregateType: "projection_run", AggregateID: run.ID,
		EventType: "projection.relation", IdempotencyKey: unit.ID, Payload: string(payload),
	}, nil
}

// relationUnitChunks mirrors the extractor's evidence filter: child chunks with
// text, ordered deterministically so unit boundaries are stable. Identical text
// inside one package is collapsed: a PDF that repeats the same pages 43 times
// must not pay for 43 identical extraction units. The first occurrence keeps
// the source chunk id; later copies are dropped.
func relationUnitChunks(pkg ingestion.EvidencePackage) ([]evidence.Chunk, int) {
	chunks := make([]evidence.Chunk, 0, len(pkg.Chunks))
	for _, chunk := range pkg.Chunks {
		if chunk.ParentID == "" || strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		chunks = append(chunks, chunk)
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].Ordinal != chunks[j].Ordinal {
			return chunks[i].Ordinal < chunks[j].Ordinal
		}
		return chunks[i].ID < chunks[j].ID
	})
	seen := make(map[string]bool, len(chunks))
	kept := chunks[:0]
	dropped := 0
	for _, chunk := range chunks {
		if chunk.ContentHash != "" {
			if seen[chunk.ContentHash] {
				dropped++
				continue
			}
			seen[chunk.ContentHash] = true
		}
		kept = append(kept, chunk)
	}
	return kept, dropped
}

func buildRelationUnits(run *models.ProjectionRun, packages []ingestion.EvidencePackage, batchSize int, now time.Time) ([]models.ProjectionUnit, error) {
	if batchSize <= 0 {
		batchSize = 32
	}
	sorted := append([]ingestion.EvidencePackage(nil), packages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].DocumentID < sorted[j].DocumentID })
	units := make([]models.ProjectionUnit, 0)
	for _, pkg := range sorted {
		chunks, dropped := relationUnitChunks(pkg)
		if dropped > 0 {
			slog.Info("relation_unit_deduped", "package_id", pkg.ID, "dropped", dropped, "kept", len(chunks))
		}
		for start := 0; start < len(chunks); start += batchSize {
			end := start + batchSize
			if end > len(chunks) {
				end = len(chunks)
			}
			ids := make([]string, 0, end-start)
			for _, chunk := range chunks[start:end] {
				ids = append(ids, chunk.ID)
			}
			encoded, err := json.Marshal(ids)
			if err != nil {
				return nil, fmt.Errorf("encode relation unit chunks: %w", err)
			}
			units = append(units, models.ProjectionUnit{
				ID:              fmt.Sprintf("%s:%s:%s:%d", run.ID, StageExtract, pkg.ID, start/batchSize),
				ProjectionRunID: run.ID, PackageID: pkg.ID, BatchIndex: start / batchSize,
				ChunkIDs: string(encoded), Status: StagePending, CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	return units, nil
}

// resetRelationUnitsForRetry restarts only what failed: a successful unit keeps
// its payload, a finished EXTRACT stage is not reopened, and a FINALIZE failure
// reopens the join instead of re-extracting the knowledge base.
func (s *Service) resetRelationUnitsForRetry(ctx context.Context, tx *gorm.DB, run *models.ProjectionRun, now time.Time) (bool, error) {
	if err := tenantdb.SetContext(tx, run.TenantID); err != nil {
		return false, err
	}
	scoped := tx
	var total, succeeded int64
	if err := scoped.Model(&models.ProjectionUnit{}).Where("projection_run_id = ?", run.ID).Count(&total).Error; err != nil {
		return false, err
	}
	if total == 0 {
		return false, nil
	}
	if err := scoped.Model(&models.ProjectionUnit{}).Where("projection_run_id = ? AND status = ?", run.ID, StageSucceeded).Count(&succeeded).Error; err != nil {
		return false, err
	}
	if succeeded == total {
		row, err := ensureStageRun(tx, run, StageFinalize)
		if err != nil {
			return false, err
		}
		if err := pipeline.RequeueInbox(tx, run.ID+":"+StageFinalize, now); err != nil {
			return false, err
		}
		if err := reopenStage(ctx, tx, run, StageFinalize, row.Attempts+1); err != nil {
			return false, err
		}
	} else {
		if _, err := ensureStageRun(tx, run, StageExtract); err != nil {
			return false, err
		}
		if err := tx.Model(&models.ProjectionStageRun{}).Where("idempotency_key = ?", run.ID+":"+StageExtract).Updates(map[string]any{
			"status": StagePending, "error_code": "", "error_message": "", "completed_at": nil, "updated_at": now,
		}).Error; err != nil {
			return false, err
		}
		var pending []models.ProjectionUnit
		if err := scoped.Where("projection_run_id = ? AND status <> ?", run.ID, StageSucceeded).
			Order("package_id, batch_index").Find(&pending).Error; err != nil {
			return false, err
		}
		if err := scoped.Model(&models.ProjectionUnit{}).Where("projection_run_id = ? AND status <> ?", run.ID, StageSucceeded).Updates(map[string]any{
			"status": StagePending, "error_code": "", "error_message": "", "completed_at": nil, "updated_at": now,
		}).Error; err != nil {
			return false, err
		}
		for _, unit := range pending {
			event, err := unitEvent(run, unit, unit.Attempts+1)
			if err != nil {
				return false, err
			}
			if err := pipeline.RequeueInbox(tx, unit.ID, now); err != nil {
				return false, err
			}
			if err := pipeline.ReopenEvent(tx, event); err != nil {
				return false, err
			}
		}
	}
	if _, _, _, err := refreshRelationUnitCounts(tx, run); err != nil {
		return false, err
	}
	// The caller already claimed the run with a conditional FAILED -> RETRY
	// update; writing the status again here could overwrite a concurrent cancel.
	return true, nil
}

type projectionStage struct {
	task       Task
	run        *models.ProjectionRun
	packages   []ingestion.EvidencePackage
	unitChunks int
}

func (p projectionStage) Start(tx *gorm.DB, now time.Time) error {
	if err := tenantdb.SetContext(tx, p.run.TenantID); err != nil {
		return err
	}
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

// SucceedMany is the fan-out path: relation COLLECT writes the unit ledger and
// returns one durable event per unit. Every other stage keeps a single event.
func (p projectionStage) SucceedMany(tx *gorm.DB, now time.Time) ([]models.OutboxEvent, error) {
	if p.run.ProjectionType != ProjectionTypeRelation || (p.task.Stage != StageCollect && p.task.Stage != StageExtract) {
		event, err := p.Succeed(tx, now)
		if err != nil || event == nil {
			return nil, err
		}
		return []models.OutboxEvent{*event}, nil
	}
	if p.task.Stage == StageCollect {
		if err := markStageRun(tx, p.run.TenantID, p.task.IdempotencyKey, StageSucceeded, "", "", now); err != nil {
			return nil, err
		}
		if _, err := ensureStageRun(tx, p.run, StageExtract); err != nil {
			return nil, err
		}
	}
	return p.fanOutUnits(tx, now)
}

func (p projectionStage) fanOutUnits(tx *gorm.DB, now time.Time) ([]models.OutboxEvent, error) {
	if len(p.packages) == 0 {
		return nil, apperror.Terminalf("no_evidence", "no current EvidencePackage is available for projection")
	}
	units, err := buildRelationUnits(p.run, p.packages, p.unitChunks, now)
	if err != nil {
		return nil, err
	}
	if len(units) == 0 {
		return nil, apperror.Terminalf("no_evidence", "no child chunk is available for relation extraction")
	}
	scoped := tx
	if err := tenantdb.SetContext(tx, p.run.TenantID); err != nil {
		return nil, err
	}
	if err := scoped.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&units, 100).Error; err != nil {
		return nil, err
	}
	var stored []models.ProjectionUnit
	if err := scoped.Where("projection_run_id = ?", p.run.ID).Order("package_id, batch_index").Find(&stored).Error; err != nil {
		return nil, err
	}
	if _, _, _, err := refreshRelationUnitCounts(tx, p.run); err != nil {
		return nil, err
	}
	events := make([]models.OutboxEvent, 0, len(stored))
	for _, unit := range stored {
		if unit.Status == StageSucceeded {
			continue
		}
		event, err := unitEvent(p.run, unit, unit.Attempts)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (p projectionStage) Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error) {
	if err := tenantdb.SetContext(tx, p.run.TenantID); err != nil {
		return nil, err
	}
	if err := tx.Model(&models.ProjectionStageRun{}).Where("idempotency_key = ?", p.task.IdempotencyKey).Updates(map[string]any{
		"status": StageSucceeded, "completed_at": &now, "error_code": "", "error_message": "", "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	next, hasNext := nextStageFor(p.run.ProjectionType, p.task.Stage)
	if !hasNext {
		return nil, nil
	}
	nextStageRow, err := ensureStageRun(tx, p.run, next)
	if err != nil {
		return nil, err
	}
	event, err := stageEvent(p.run, next, nextStageRow.Attempts, p.task.TraceParent)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (p projectionStage) Fail(tx *gorm.DB, code, message string, now time.Time) error {
	if err := tenantdb.SetContext(tx, p.run.TenantID); err != nil {
		return err
	}
	if err := tx.Model(&models.ProjectionStageRun{}).Where("idempotency_key = ?", p.task.IdempotencyKey).Updates(map[string]any{
		"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return tenantdb.MustScoped(tx, p.run.TenantID).Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", p.run.ID, ProjectionCancelled).Updates(map[string]any{
		"status": failedStatusFor(p.run.ProjectionType), "error_code": code, "error_message": message, "updated_at": now,
	}).Error
}

// projectionUnitStage adapts one extraction unit to the shared runtime. The
// unit row carries attempts and the payload; the run row carries progress.
type projectionUnitStage struct {
	task    Task
	run     *models.ProjectionRun
	payload *string
	// terminal marks a failure the queue will not retry. Retryable failures
	// leave the run in RETRY_RELATION; only an exhausted budget turns it into
	// FAILED_RELATION.
	terminal bool
}

// Start records the unit attempt. A failed run stays failed until the retried
// unit succeeds; otherwise one terminal unit failure would be hidden by a later
// unit starting and the run could hang without FINALIZE.
func (p projectionUnitStage) Start(tx *gorm.DB, now time.Time) error {
	if err := tenantdb.SetContext(tx, p.run.TenantID); err != nil {
		return err
	}
	var unit models.ProjectionUnit
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&unit, "id = ?", p.task.UnitID).Error; err != nil {
		return err
	}
	if unit.ProjectionRunID != p.run.ID {
		return fmt.Errorf("projection unit does not belong to run")
	}
	if err := tx.Model(&models.ProjectionUnit{}).Where("id = ?", unit.ID).Updates(map[string]any{
		"status": StageProcessing, "attempts": gorm.Expr("attempts + 1"), "started_at": &now,
		"error_code": "", "error_message": "", "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return tenantdb.MustScoped(tx, p.run.TenantID).Model(&models.ProjectionRun{}).
		Where("id = ? AND status IN ?", p.run.ID, []string{RelationPending, RelationRetrying}).
		Updates(map[string]any{"status": RelationExtracting, "updated_at": now}).Error
}

func (p projectionUnitStage) Succeed(tx *gorm.DB, now time.Time) (*models.OutboxEvent, error) {
	run, err := lockProjectionRun(tx, p.run.TenantID, p.run.ID)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"status": StageSucceeded, "completed_at": &now, "error_code": "", "error_message": "", "updated_at": now,
	}
	if p.payload != nil {
		updates["payload"] = *p.payload
	}
	if err := tx.Model(&models.ProjectionUnit{}).Where("id = ?", p.task.UnitID).Updates(updates).Error; err != nil {
		return nil, err
	}
	total, succeeded, _, err := refreshRelationUnitCounts(tx, run)
	if err != nil {
		return nil, err
	}
	if run.Status == ProjectionCancelled || run.Status == RelationFailed || succeeded < total || total == 0 {
		return nil, nil
	}
	var extractStage models.ProjectionStageRun
	if err := tx.First(&extractStage, "idempotency_key = ?", run.ID+":"+StageExtract).Error; err != nil {
		return nil, err
	}
	if extractStage.Status == StageSucceeded {
		return nil, nil
	}
	if err := markStageRun(tx, run.TenantID, run.ID+":"+StageExtract, StageSucceeded, "", "", now); err != nil {
		return nil, err
	}
	// The run may still read FAILED_RELATION from an earlier unit failure; a
	// finished extraction must accept FINALIZE, so clear it before the event.
	if err := tenantdb.MustScoped(tx, run.TenantID).Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", run.ID, ProjectionCancelled).Updates(map[string]any{
		"status": RelationExtracting, "error_code": "", "error_message": "", "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	finalizeStage, err := ensureStageRun(tx, run, StageFinalize)
	if err != nil {
		return nil, err
	}
	event, err := stageEvent(run, StageFinalize, finalizeStage.Attempts, p.task.TraceParent)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (p projectionUnitStage) Fail(tx *gorm.DB, code, message string, now time.Time) error {
	run, err := lockProjectionRun(tx, p.run.TenantID, p.run.ID)
	if err != nil {
		return err
	}
	if err := tx.Model(&models.ProjectionUnit{}).Where("id = ?", p.task.UnitID).Updates(map[string]any{
		"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if _, _, _, err := refreshRelationUnitCounts(tx, run); err != nil {
		return err
	}
	// A cancelled or already-terminal run must not be revived by a late
	// in-flight failure; the run status is the operator-visible truth.
	if run.Status == ProjectionCancelled || run.Status == RelationFailed {
		return nil
	}
	if err := markStageRun(tx, run.TenantID, run.ID+":"+StageExtract, StageFailed, code, message, now); err != nil {
		return err
	}
	status := RelationRetrying
	if p.terminal {
		status = RelationFailed
	}
	return tenantdb.MustScoped(tx, run.TenantID).Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", run.ID, ProjectionCancelled).Updates(map[string]any{
		"status": status, "error_code": code, "error_message": message, "updated_at": now,
	}).Error
}

func (s *Service) processRelationUnit(ctx context.Context, task Task) error {
	if s.relation == nil {
		return apperror.Terminalf("relation_extractor_missing", "relation projection is not configured")
	}
	claimed, err := s.runtime().Claim(ctx, task.UnitID, "projection.relation.unit", 0)
	if errors.Is(err, inboxlease.ErrStaleDelivery) {
		slog.Info("stale_delivery_acked", "idempotency_key", task.UnitID)
		return nil
	}
	if err != nil || !claimed {
		return err
	}
	processCtx, releaseLease, retainLease, leaseLost := s.runtime().Heartbeat(ctx, task.UnitID)
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
		return s.finishInbox(ctx, task.UnitID, inboxFailed, err)
	}
	if run.ProjectionType != ProjectionTypeRelation {
		return s.finishInbox(ctx, task.UnitID, inboxFailed, apperror.Terminalf("projection_type_mismatch", "projection type mismatch"))
	}
	if run.Status == RelationReady || run.Status == ProjectionCancelled {
		return s.finishInbox(ctx, task.UnitID, inboxSucceeded, nil)
	}
	var unit models.ProjectionUnit
	if err := s.db.WithContext(ctx).First(&unit, "id = ?", task.UnitID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s.finishInbox(ctx, task.UnitID, inboxSucceeded, nil)
		}
		return s.finishInbox(ctx, task.UnitID, inboxFailed, err)
	}
	if unit.ProjectionRunID != run.ID {
		return s.finishInbox(ctx, task.UnitID, inboxFailed, apperror.Terminalf("unit_run_mismatch", "projection unit does not belong to run"))
	}
	if unit.Status == StageSucceeded {
		return s.finishInbox(ctx, task.UnitID, inboxSucceeded, nil)
	}
	if err := validateRelationUnitTransition(run.Status); err != nil {
		return s.finishInbox(ctx, task.UnitID, inboxFailed, err)
	}
	stage := projectionUnitStage{task: task, run: run}
	if err := s.runtime().Start(ctx, stage); err != nil {
		return err
	}
	unitCtx := processCtx
	if s.unitTimeout > 0 {
		var cancel context.CancelFunc
		unitCtx, cancel = context.WithTimeout(processCtx, s.unitTimeout)
		defer cancel()
	}
	payload, processErr := s.extractUnitPayload(unitCtx, run, unit)
	if leaseLost() {
		return fmt.Errorf("inbox lease lost for %s: %w", task.UnitID, context.Canceled)
	}
	if errors.Is(processErr, errRelationEvidenceChanged) {
		return s.cancelUnitForSourceChange(ctx, task, run)
	}
	if processErr != nil {
		terminal := apperror.IsTerminal(processErr)
		code, message := splitError(processErr)
		stage.terminal = terminal
		if err := s.runtime().Fail(ctx, stage, code, message); err != nil {
			return err
		}
		if terminal {
			return s.finishInbox(ctx, task.UnitID, inboxFailed, processErr)
		}
		if err := pipeline.RetainFailedInbox(ctx, s.db, task.UnitID, processErr.Error()); err != nil {
			return err
		}
		retainOnReturn = true
		return processErr
	}
	stage.payload = &payload
	return s.runtime().Complete(ctx, task.UnitID, stage)
}

func (s *Service) extractUnitPayload(ctx context.Context, run *models.ProjectionRun, unit models.ProjectionUnit) (string, error) {
	pkg, found, err := s.evidence.CurrentPackage(ctx, run.TenantID, run.KnowledgeBaseID, unit.PackageID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errRelationEvidenceChanged
	}
	var chunkIDs []string
	if err := json.Unmarshal([]byte(unit.ChunkIDs), &chunkIDs); err != nil {
		return "", apperror.Terminalf("unit_chunks_invalid", "unit %s chunk list is unreadable", unit.ID)
	}
	byID := make(map[string]evidence.Chunk, len(pkg.Chunks))
	for _, chunk := range pkg.Chunks {
		byID[chunk.ID] = chunk
	}
	chunks := make([]evidence.Chunk, 0, len(chunkIDs))
	for _, id := range chunkIDs {
		chunk, ok := byID[id]
		if !ok {
			return "", apperror.Terminalf("evidence_chunk_missing", "unit %s chunk %s is no longer in the package", unit.ID, id)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return "", apperror.Terminalf("empty_relation_unit", "unit %s has no chunks", unit.ID)
	}
	extracted, stats, err := s.relation.ExtractUnit(ctx, pkg, unit.ID, chunks)
	if err != nil {
		return "", err
	}
	s.recordGeneration(ProjectionTypeRelation, stats.Generated, stats.Reused)
	encoded, err := json.Marshal(extracted)
	if err != nil {
		return "", fmt.Errorf("encode relation unit: %w", err)
	}
	return string(encoded), nil
}

// AbandonRelationUnit is the queue's terminal hook: the unit exhausted its retry
// budget, so the run can no longer make progress without an operator retry. The
// unit stays FAILED and the inbox is released so Retry can claim it again.
func (s *Service) AbandonRelationUnit(ctx context.Context, tenantID, runID, unitID string, cause error) error {
	if tenantID == "" || runID == "" || unitID == "" {
		return nil
	}
	var run models.ProjectionRun
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if run.ProjectionType != ProjectionTypeRelation || run.Status == ProjectionCancelled || run.Status == RelationReady {
		return nil
	}
	code, message := splitError(cause)
	now := time.Now()
	return tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if err := tenantdb.SetContext(tx, tenantID); err != nil {
			return err
		}
		var unit models.ProjectionUnit
		if err := tx.First(&unit, "id = ? AND projection_run_id = ?", unitID, runID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if unit.Status == StageSucceeded {
			return nil
		}
		if err := tx.Model(&models.ProjectionUnit{}).Where("id = ?", unit.ID).Updates(map[string]any{
			"status": StageFailed, "error_code": code, "error_message": message, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if _, _, _, err := refreshRelationUnitCounts(tx, &run); err != nil {
			return err
		}
		if err := markStageRun(tx, tenantID, runID+":"+StageExtract, StageFailed, code, message, now); err != nil {
			return err
		}
		if err := tenantdb.MustScoped(tx, tenantID).Model(&models.ProjectionRun{}).Where("id = ? AND status <> ?", runID, ProjectionCancelled).Updates(map[string]any{
			"status": RelationFailed, "error_code": code, "error_message": message, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return pipeline.FailProcessingInbox(ctx, tx, unitID, message)
	})
}

// cancelUnitForSourceChange closes a run whose evidence moved after COLLECT.
// The unit row is marked failed with the stable source_changed code so a
// cancelled run does not keep a PROCESSING unit.
func (s *Service) cancelUnitForSourceChange(ctx context.Context, task Task, run *models.ProjectionRun) error {
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return s.finishInbox(ctx, task.UnitID, inboxFailed, err)
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return s.finishInbox(ctx, task.UnitID, inboxFailed, err)
	}
	sourceHash := s.relationSourceHash(packages, knowledgeBase.ACLVersion)
	if err := s.cancelForSourceChange(ctx, run, knowledgeBase, packages, sourceHash); err != nil {
		return err
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.ProjectionUnit{}).Where("id = ?", task.UnitID).Updates(map[string]any{
		"status": StageFailed, "error_code": "source_changed", "error_message": "evidence changed during relation projection", "updated_at": now,
	}).Error; err != nil {
		return err
	}
	return s.finishInbox(ctx, task.UnitID, inboxSucceeded, nil)
}

func (s *Service) finalizeRelation(ctx context.Context, run *models.ProjectionRun) error {
	if superseded, err := s.supersedeStaleRun(ctx, run); err != nil {
		return err
	} else if superseded {
		return nil
	}
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return err
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return apperror.Terminal("knowledge_base_missing", err)
	}
	sourceHash := s.relationSourceHash(packages, knowledgeBase.ACLVersion)
	if run.SourceHash != "" && run.SourceHash != sourceHash {
		return s.cancelForSourceChange(ctx, run, knowledgeBase, packages, sourceHash)
	}
	projection, err := s.mergeUnits(ctx, run)
	if err != nil {
		return err
	}
	if len(projection.Relations) == 0 {
		return apperror.Terminalf("empty_relation_projection", "extraction produced no sourced relation")
	}
	if err := s.persistSnapshot(ctx, run, knowledgeBase, packages, projection, time.Now()); err != nil {
		return err
	}
	snapshot, err := s.loadRunSnapshot(ctx, run.ID)
	if err != nil {
		return apperror.Terminal("snapshot_missing", err)
	}
	if err := s.validateSnapshotSources(ctx, snapshot.ID); err != nil {
		return err
	}
	now := time.Now()
	if s.store != nil {
		if err := s.store.Update(ctx, snapshot.ID, relationstore.SnapshotUpdate{
			Status: models.GraphSnapshotPending, SourceHash: &sourceHash, ACLVersion: &knowledgeBase.ACLVersion, At: now,
		}); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": RelationReady, "source_hash": sourceHash, "acl_version": knowledgeBase.ACLVersion,
		"completed_at": &now, "error_code": "", "error_message": "", "updated_at": now,
	}).Error
}

// mergeUnits rebuilds the extraction package from the durable unit payloads.
// No model call happens here; MergeExtraction normalizes exactly as the old
// whole-knowledge-base path did.
func (s *Service) mergeUnits(ctx context.Context, run *models.ProjectionRun) (RelationProjectionPackage, error) {
	var units []models.ProjectionUnit
	if err := s.db.WithContext(ctx).Where("projection_run_id = ?", run.ID).
		Order("package_id, batch_index").Find(&units).Error; err != nil {
		return RelationProjectionPackage{}, err
	}
	if len(units) == 0 {
		return RelationProjectionPackage{}, apperror.Terminalf("no_units", "relation projection run has no extraction units")
	}
	result := RelationProjectionPackage{
		SchemaVersion: SchemaRelationProjection, TenantID: run.TenantID,
		KnowledgeBaseID: run.KnowledgeBaseID, ACLVersion: run.ACLVersion,
	}
	merged := 0
	for _, unit := range units {
		if unit.Status != StageSucceeded {
			continue
		}
		if unit.Payload == nil || strings.TrimSpace(*unit.Payload) == "" {
			return RelationProjectionPackage{}, apperror.Terminalf("unit_payload_missing", "unit %s has no extraction payload", unit.ID)
		}
		var extracted RelationProjectionPackage
		if err := json.Unmarshal([]byte(*unit.Payload), &extracted); err != nil {
			return RelationProjectionPackage{}, apperror.Terminalf("unit_payload_invalid", "unit %s payload is unreadable", unit.ID)
		}
		result.Entities = append(result.Entities, extracted.Entities...)
		result.Relations = append(result.Relations, extracted.Relations...)
		merged++
	}
	if merged != len(units) {
		return RelationProjectionPackage{}, apperror.Terminalf("units_incomplete", "%d of %d extraction units succeeded", merged, len(units))
	}
	return relation.MergeExtraction(result), nil
}

// cancelForSourceChange supersedes the run when the evidence it collected no
// longer matches. Current packages, when present, get a replacement run.
func (s *Service) cancelForSourceChange(ctx context.Context, run *models.ProjectionRun, knowledgeBase models.KnowledgeBase, packages []ingestion.EvidencePackage, sourceHash string) error {
	now := time.Now()
	if s.store != nil {
		if snapshot, err := s.store.SnapshotByRun(ctx, run.ID); err == nil {
			if err := s.store.Update(ctx, snapshot.ID, relationstore.SnapshotUpdate{Status: models.GraphSnapshotSuperseded, At: now}); err != nil {
				return err
			}
		} else if !errors.Is(err, relationstore.ErrNotFound) {
			return err
		}
	}
	return tenantdb.InTx(ctx, s.db, knowledgeBase.TenantID, func(tx *gorm.DB) error {
		status, code := ProjectionSuperseded, "epoch_superseded"
		message := "evidence changed during relation projection; catch-up will rebuild"
		if len(packages) == 0 {
			// The evidence disappeared mid-run and no replacement can be
			// scheduled. Cancel instead of indexing an empty slice.
			status, code = ProjectionCancelled, "source_removed"
			message = "evidence was removed during relation projection; rebuild is required"
		}
		return tx.Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status": status, "error_code": code,
			"error_message": message,
			"completed_at":  &now, "updated_at": now,
		}).Error
	})
}

func lockProjectionRun(tx *gorm.DB, tenantID, runID string) (*models.ProjectionRun, error) {
	var run models.ProjectionRun
	if err := tenantdb.MustScoped(tx, tenantID).Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, "id = ?", runID).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

// refreshRelationUnitCounts recomputes the run counters from the unit ledger
// under the run row lock. Recomputing instead of incrementing keeps retries and
// duplicate completions from drifting the progress numbers.
func refreshRelationUnitCounts(tx *gorm.DB, run *models.ProjectionRun) (int, int, int, error) {
	if err := tenantdb.SetContext(tx, run.TenantID); err != nil {
		return 0, 0, 0, err
	}
	var counts struct {
		Total     int
		Succeeded int
		Failed    int
	}
	err := tx.Model(&models.ProjectionUnit{}).
		Select("COUNT(*) AS total, COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS succeeded, COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS failed", StageSucceeded, StageFailed).
		Where("projection_run_id = ?", run.ID).Scan(&counts).Error
	if err != nil {
		return 0, 0, 0, err
	}
	err = tenantdb.MustScoped(tx, run.TenantID).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"unit_total": counts.Total, "unit_succeeded": counts.Succeeded, "unit_failed": counts.Failed,
	}).Error
	if err != nil {
		return 0, 0, 0, err
	}
	run.UnitTotal, run.UnitSucceeded, run.UnitFailed = counts.Total, counts.Succeeded, counts.Failed
	return counts.Total, counts.Succeeded, counts.Failed, nil
}

func ensureStageRun(tx *gorm.DB, run *models.ProjectionRun, stage string) (models.ProjectionStageRun, error) {
	key := run.ID + ":" + stage
	if err := tenantdb.SetContext(tx, run.TenantID); err != nil {
		return models.ProjectionStageRun{}, err
	}
	var row models.ProjectionStageRun
	err := tx.Where("idempotency_key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = models.ProjectionStageRun{
			ProjectionRunID: run.ID, ProjectionType: run.ProjectionType, Stage: stage,
			Status: StagePending, IdempotencyKey: key,
		}
		if err := tx.Create(&row).Error; err != nil {
			return row, err
		}
		return row, nil
	}
	return row, err
}

func markStageRun(tx *gorm.DB, tenantID, key, status, code, message string, now time.Time) error {
	if err := tenantdb.SetContext(tx, tenantID); err != nil {
		return err
	}
	updates := map[string]any{
		"status": status, "error_code": code, "error_message": message, "updated_at": now,
	}
	if status == StageSucceeded {
		updates["completed_at"] = &now
	}
	return tx.Model(&models.ProjectionStageRun{}).Where("idempotency_key = ?", key).Updates(updates).Error
}

// persistSnapshot writes one immutable graph snapshot. Relations without a
// valid source chunk are dropped and counted; they never reach a snapshot.
func (s *Service) persistSnapshot(ctx context.Context, run *models.ProjectionRun, knowledgeBase models.KnowledgeBase, packages []ingestion.EvidencePackage, projection RelationProjectionPackage, now time.Time) error {
	if s.store == nil {
		return fmt.Errorf("relation store is not configured")
	}
	chunks := evidenceSourceMap(packages)
	entities := make(map[string]ExtractedEntity, len(projection.Entities))
	for _, entity := range projection.Entities {
		entities[entity.Key] = entity
	}
	keptRelations := make([]ExtractedRelation, 0, len(projection.Relations))
	dropped := 0
	for _, relation := range projection.Relations {
		sources := filterSources(relation.SourceChunkIDs, chunks)
		if len(sources) == 0 {
			dropped++
			continue
		}
		relation.SourceChunkIDs = sources
		keptRelations = append(keptRelations, relation)
		// A relation endpoint must exist even when the model omitted it.
		for _, key := range []string{relation.SubjectKey, relation.ObjectKey} {
			if _, ok := entities[key]; !ok {
				entities[key] = ExtractedEntity{Key: key, Name: key}
			}
		}
	}
	if len(keptRelations) == 0 {
		return apperror.Terminalf("unsourced_relations", "every extracted relation lacked a source chunk")
	}

	snapshotID := stableID("gs", knowledgeBase.ID, run.ID)
	entityIDs := make(map[string]string, len(entities))
	for key := range entities {
		entityIDs[key] = stableID("ge", snapshotID, key)
	}
	keys := make([]string, 0, len(entities))
	for key := range entities {
		keys = append(keys, key)
	}
	sortStrings(keys)

	entityRows := make([]relationstore.Entity, 0, len(entities))
	aliasRows := make([]relationstore.Alias, 0)
	for _, key := range keys {
		entity := entities[key]
		entityRows = append(entityRows, relationstore.Entity{
			ID: entityIDs[key], TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID,
			CanonicalKey: key, Name: entity.Name, EntityType: entity.Type,
			Properties: entity.Properties, ACLVersion: knowledgeBase.ACLVersion,
		})
		seenAliases := map[string]bool{}
		for _, alias := range entity.Aliases {
			normalized := relation.NormalizeEntityKey(alias)
			if normalized == "" || normalized == key || seenAliases[normalized] {
				continue
			}
			seenAliases[normalized] = true
			rule := entity.AliasRules[normalized]
			if rule == "" {
				rule = "declared_alias"
			}
			mergedKey := ""
			if rule == "normalized_merge" {
				mergedKey = normalized
			}
			aliasRows = append(aliasRows, relationstore.Alias{
				TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID,
				EntityID: entityIDs[key], Alias: alias, Normalized: normalized,
				Rule: rule, MergedKey: mergedKey, CreatedAt: now,
			})
		}
	}

	relationRows := make([]relationstore.Relation, 0, len(keptRelations))
	sourceRows := make([]relationstore.Source, 0)
	for _, relation := range keptRelations {
		relationID := stableID("gr", snapshotID, relation.SubjectKey, strings.ToLower(relation.Predicate), relation.ObjectKey)
		confidence := relation.Confidence
		if confidence <= 0 {
			confidence = 1
		}
		relationRows = append(relationRows, relationstore.Relation{
			ID: relationID, TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID,
			SubjectID: entityIDs[relation.SubjectKey], Predicate: relation.Predicate, ObjectID: entityIDs[relation.ObjectKey],
			Properties: relation.Properties, Confidence: confidence, ACLVersion: knowledgeBase.ACLVersion,
		})
		for _, chunkID := range relation.SourceChunkIDs {
			chunk, ok := chunks[chunkID]
			if !ok {
				continue
			}
			sourceRows = append(sourceRows, relationstore.Source{
				TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID,
				RelationID: relationID, DocumentID: chunk.documentID, DocumentVersionID: chunk.documentVersionID,
				ChunkID: chunkID, SourcePath: chunk.sourcePath, HeadingPath: chunk.headingPath, PageNumber: chunk.pageNumber,
			})
		}
	}

	var stats struct {
		Entities                 int `json:"entities"`
		Relations                int `json:"relations"`
		Aliases                  int `json:"aliases"`
		DroppedUnsourcedRelation int `json:"droppedUnsourcedRelations"`
		Chunks                   int `json:"chunks"`
	}
	stats.Entities = len(entityRows)
	stats.Relations = len(relationRows)
	stats.Aliases = len(aliasRows)
	stats.DroppedUnsourcedRelation = dropped
	for _, relation := range keptRelations {
		stats.Chunks += len(relation.SourceChunkIDs)
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("encode graph stats: %w", err)
	}

	if err := s.store.ReplaceRunSnapshot(ctx, relationstore.SnapshotWrite{
		Snapshot: relationstore.Snapshot{
			ID: snapshotID, TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID,
			ProjectionRunID: run.ID, Status: models.GraphSnapshotBuilding, SourceHash: run.SourceHash,
			ACLVersion: knowledgeBase.ACLVersion, TargetEpoch: run.TargetEpoch,
			Model: s.relation.Model(), PromptVersion: s.relation.PromptVersion(),
			Stats: string(statsJSON), CreatedAt: now, UpdatedAt: now,
		},
		Entities: entityRows, Aliases: aliasRows, Relations: relationRows, Sources: sourceRows,
	}); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"acl_version": knowledgeBase.ACLVersion, "model": s.relation.Model(), "prompt_version": s.relation.PromptVersion(),
		"error_code": "", "error_message": "", "updated_at": now,
	}).Error
}

func (s *Service) loadRunSnapshot(ctx context.Context, runID string) (*relationstore.Snapshot, error) {
	if s.store == nil {
		return nil, fmt.Errorf("relation store is not configured")
	}
	return s.store.SnapshotByRun(ctx, runID)
}

// validateSnapshotSources enforces the release gate: every relation has at
// least one source and every source is still the current, evidence-ready chunk.
func (s *Service) validateSnapshotSources(ctx context.Context, snapshotID string) error {
	if s.store == nil {
		return fmt.Errorf("relation store is not configured")
	}
	refs, err := s.store.SourceRefs(ctx, snapshotID)
	if err != nil {
		return err
	}
	if refs.SourcelessRelations > 0 {
		return apperror.Terminalf("untraceable_relation", "%d relations have no source chunk", refs.SourcelessRelations)
	}
	valid, err := s.currentEvidence(ctx, refs.Refs)
	if err != nil {
		return err
	}
	invalid := 0
	for _, ref := range refs.Refs {
		if !valid[sourceKey(ref.RelationID, ref.ChunkID)] {
			invalid++
		}
	}
	if invalid > 0 {
		return apperror.Terminalf("stale_relation_source", "%d relation sources are no longer current evidence", invalid)
	}
	return nil
}

type evidenceChunkSource struct {
	documentID        string
	documentVersionID string
	sourcePath        string
	headingPath       string
	pageNumber        int
}

func evidenceSourceMap(packages []ingestion.EvidencePackage) map[string]evidenceChunkSource {
	result := map[string]evidenceChunkSource{}
	for _, pkg := range packages {
		for _, chunk := range pkg.Chunks {
			result[chunk.ID] = evidenceChunkSource{
				documentID: pkg.DocumentID, documentVersionID: pkg.DocumentVersionID,
				sourcePath: chunk.SourcePath, headingPath: chunk.HeadingPath, pageNumber: chunk.PageNumber,
			}
		}
	}
	return result
}

func filterSources(ids []string, allowed map[string]evidenceChunkSource) []string {
	seen := map[string]bool{}
	var result []string
	for _, id := range ids {
		if _, ok := allowed[id]; !ok || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
