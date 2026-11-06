package knowledgeprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgeprojection/relation"
	"github.com/example/rag-platform/internal/pipeline"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/relationstore"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

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

func (s *Service) processRelation(ctx context.Context, task Task) error {
	if task.SchemaVersion != SchemaRelationTask || task.ProjectionRunID == "" || task.Stage == "" || task.ProjectionType != ProjectionTypeRelation {
		return apperror.Terminalf("invalid_relation_projection_task_envelope", "invalid relation projection task envelope")
	}
	if s.relation == nil {
		return apperror.Terminalf("relation_extractor_missing", "relation projection is not configured")
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
	switch task.Stage {
	case StageCollect:
		processErr = s.collectRelation(processCtx, run)
	case StageExtract:
		processErr = s.extractRelation(processCtx, run)
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
	return s.runtime().Complete(ctx, task.IdempotencyKey, projectionStage{task: task, run: run})
}

func (s *Service) collectRelation(ctx context.Context, run *models.ProjectionRun) error {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return apperror.Terminal("knowledge_base_missing", err)
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return apperror.Terminal("invalid_indexing_strategy", err)
	}
	if !strategy.GraphEnabled {
		return apperror.Terminalf("graph_disabled", "relation projection is not enabled for this knowledge base")
	}
	packages, err := s.loadCurrentPackages(ctx, run.TenantID, run.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return apperror.Terminalf("no_evidence", "no current EvidencePackage is available for projection")
	}
	sourceHash := s.relationSourceHash(packages, knowledgeBase.ACLVersion)
	return s.db.WithContext(ctx).Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"source_hash": sourceHash, "acl_version": knowledgeBase.ACLVersion,
		"model": s.relation.Model(), "prompt_version": s.relation.PromptVersion(),
		"error_code": "", "error_message": "", "updated_at": time.Now(),
	}).Error
}

func (s *Service) relationSourceHash(packages []ingestion.EvidencePackage, aclVersion int64) string {
	hash, _ := packagesSourceHash(packages, aclVersion, s.relation.PromptVersion(), s.relation.Model())
	return hash
}

func (s *Service) extractRelation(ctx context.Context, run *models.ProjectionRun) error {
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
	projection, stats, err := s.relation.Extract(ctx, knowledgeBase.TenantID, knowledgeBase.ID, knowledgeBase.ACLVersion, packages)
	if err != nil {
		return err
	}
	if len(projection.Relations) == 0 {
		return apperror.Terminalf("empty_relation_projection", "extraction produced no sourced relation")
	}
	s.recordGeneration(ProjectionTypeRelation, stats.Generated, stats.Reused)
	return s.persistSnapshot(ctx, run, knowledgeBase, packages, projection, time.Now())
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
			ACLVersion: knowledgeBase.ACLVersion, Model: s.relation.Model(), PromptVersion: s.relation.PromptVersion(),
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

func (s *Service) finalizeRelation(ctx context.Context, run *models.ProjectionRun) error {
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
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			status, code := ProjectionCancelled, "source_changed"
			message := "evidence changed during relation projection; replacement run created"
			if len(packages) == 0 {
				// The evidence disappeared mid-run and no replacement can be
				// scheduled. Cancel instead of indexing an empty slice.
				code = "source_removed"
				message = "evidence was removed during relation projection; rebuild is required"
			}
			if err := tx.Model(&models.ProjectionRun{}).Where("id = ?", run.ID).Updates(map[string]any{
				"status": status, "error_code": code,
				"error_message": message,
				"completed_at":  &now, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if len(packages) == 0 {
				return nil
			}
			replacement := packages[len(packages)-1]
			return s.insertRunFor(tx, knowledgeBase, ProjectionTypeRelation, replacement.ID, sourceHash, now)
		})
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
