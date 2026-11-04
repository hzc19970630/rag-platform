package knowledgeprojection

import (
	"context"
	"errors"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ComputeJoin is the ProjectionJoin rule as a pure function. Each enabled
// projection is evaluated independently: wiki and relation never block one
// another, but KNOWLEDGE_READY requires every enabled projection to describe
// the current evidence.
func ComputeJoin(wikiEnabled, graphEnabled bool, latestWiki, latestRelation *models.ProjectionRun, expectedWikiHash, expectedRelationHash string) string {
	if !wikiEnabled && !graphEnabled {
		return JoinEvidenceReady
	}
	if wikiEnabled && !projectionCurrent(latestWiki, expectedWikiHash) {
		return JoinKnowledgePartial
	}
	if graphEnabled && !projectionCurrent(latestRelation, expectedRelationHash) {
		return JoinKnowledgePartial
	}
	return JoinKnowledgeReady
}

func projectionCurrent(run *models.ProjectionRun, expectedHash string) bool {
	if run == nil {
		return false
	}
	return run.Status == readyStatusFor(run.ProjectionType) && run.SourceHash == expectedHash
}

// JoinStatus is the public port used to display the aggregated projection
// state. It never exposes internal rows: the caller only learns EVIDENCE_READY,
// KNOWLEDGE_READY or KNOWLEDGE_PARTIAL.
func (s *Service) JoinStatus(ctx context.Context, tenantID, knowledgeBaseID string) (string, error) {
	knowledgeBase, strategy, err := s.loadJoinScope(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return "", err
	}
	evaluation, err := s.evaluateJoin(ctx, knowledgeBase, strategy)
	if err != nil {
		return "", err
	}
	return evaluation.status, nil
}

// CapabilityStatus reports each projection independently so that a query is
// gated only by the capabilities it declares. A failed relation run must not
// fail a wiki-only or evidence-only query.
func (s *Service) CapabilityStatus(ctx context.Context, tenantID, knowledgeBaseID string) (retrieval.CapabilityStatus, error) {
	knowledgeBase, strategy, err := s.loadJoinScope(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return retrieval.CapabilityStatus{}, err
	}
	evaluation, err := s.evaluateJoin(ctx, knowledgeBase, strategy)
	if err != nil {
		return retrieval.CapabilityStatus{}, err
	}
	return retrieval.CapabilityStatus{WikiReady: evaluation.wikiReady, RelationReady: evaluation.relationReady}, nil
}

type joinEvaluation struct {
	status        string
	wikiReady     bool
	relationReady bool
}

func (s *Service) loadJoinScope(ctx context.Context, tenantID, knowledgeBaseID string) (models.KnowledgeBase, retrieval.IndexingStrategy, error) {
	knowledgeBase, err := s.loadKnowledgeBase(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return models.KnowledgeBase{}, retrieval.IndexingStrategy{}, err
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return models.KnowledgeBase{}, retrieval.IndexingStrategy{}, err
	}
	return knowledgeBase, strategy, nil
}

func (s *Service) joinStatus(ctx context.Context, knowledgeBase models.KnowledgeBase, strategy retrieval.IndexingStrategy) (string, error) {
	evaluation, err := s.evaluateJoin(ctx, knowledgeBase, strategy)
	if err != nil {
		return "", err
	}
	return evaluation.status, nil
}

// evaluateJoin computes the aggregate status once, together with the
// per-projection readiness the query gate needs. A disabled projection is
// reported ready so callers can gate only declared capabilities. Expected
// hashes come from the materialized state row; only a missing or stale row
// triggers a full package scan.
func (s *Service) evaluateJoin(ctx context.Context, knowledgeBase models.KnowledgeBase, strategy retrieval.IndexingStrategy) (joinEvaluation, error) {
	if !strategy.WikiEnabled && !strategy.GraphEnabled {
		return joinEvaluation{status: JoinEvidenceReady, wikiReady: true, relationReady: true}, nil
	}
	state, err := s.ensureProjectionState(ctx, knowledgeBase, strategy)
	if err != nil {
		return joinEvaluation{}, err
	}
	evaluation := joinEvaluation{wikiReady: true, relationReady: true}
	var latestWiki, latestRelation *models.ProjectionRun
	if strategy.WikiEnabled {
		latestWiki, err = s.latestRun(ctx, knowledgeBase.TenantID, knowledgeBase.ID, ProjectionTypeWiki)
		if err != nil {
			return joinEvaluation{}, err
		}
		evaluation.wikiReady = projectionCurrent(latestWiki, state.WikiExpectedHash)
	}
	if strategy.GraphEnabled {
		if s.relation != nil {
			latestRelation, err = s.latestRun(ctx, knowledgeBase.TenantID, knowledgeBase.ID, ProjectionTypeRelation)
			if err != nil {
				return joinEvaluation{}, err
			}
			evaluation.relationReady = projectionCurrent(latestRelation, state.RelationExpectedHash)
		}
	}
	evaluation.status = ComputeJoin(strategy.WikiEnabled, strategy.GraphEnabled, latestWiki, latestRelation, state.WikiExpectedHash, state.RelationExpectedHash)
	return evaluation, nil
}

// ensureProjectionState returns a state row whose hashes match the current
// evidence, ACL and generator configuration. A stale row is refreshed with one
// package scan and written back; the common path is a single indexed read.
func (s *Service) ensureProjectionState(ctx context.Context, knowledgeBase models.KnowledgeBase, strategy retrieval.IndexingStrategy) (models.KnowledgeProjectionState, error) {
	state, found, err := s.loadProjectionState(ctx, knowledgeBase.TenantID, knowledgeBase.ID)
	if err != nil {
		return models.KnowledgeProjectionState{}, err
	}
	wikiPrompt, wikiModel, err := s.wikiConfiguration()
	if err != nil {
		return models.KnowledgeProjectionState{}, err
	}
	relationPrompt, relationModel := "", ""
	if s.relation != nil {
		relationPrompt, relationModel = s.relation.PromptVersion(), s.relation.Model()
	}
	if found && s.projectionStateCurrent(state, knowledgeBase, strategy, wikiPrompt, wikiModel, relationPrompt, relationModel) {
		changed, err := s.evidenceChangedSince(ctx, knowledgeBase, state.UpdatedAt)
		if err != nil {
			return models.KnowledgeProjectionState{}, err
		}
		if !changed {
			return state, nil
		}
	}

	packages, err := s.loadCurrentPackages(ctx, knowledgeBase.TenantID, knowledgeBase.ID)
	if err != nil {
		return models.KnowledgeProjectionState{}, err
	}
	if !found || state.ACLVersion != knowledgeBase.ACLVersion {
		state = models.KnowledgeProjectionState{TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID}
	}
	state.ACLVersion = knowledgeBase.ACLVersion
	if len(packages) > 0 {
		if strategy.WikiEnabled && (state.WikiExpectedHash == "" || state.WikiPromptVersion != wikiPrompt || state.WikiModel != wikiModel) {
			state.WikiExpectedHash, _ = packagesSourceHash(packages, knowledgeBase.ACLVersion, wikiPrompt, wikiModel)
			state.WikiPromptVersion, state.WikiModel = wikiPrompt, wikiModel
		}
		if strategy.GraphEnabled && s.relation != nil && (state.RelationExpectedHash == "" || state.RelationPromptVersion != relationPrompt || state.RelationModel != relationModel) {
			state.RelationExpectedHash = s.relationSourceHash(packages, knowledgeBase.ACLVersion)
			state.RelationPromptVersion, state.RelationModel = relationPrompt, relationModel
		}
	}
	state.UpdatedAt = time.Now()
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&state).Error; err != nil {
		return models.KnowledgeProjectionState{}, err
	}
	return state, nil
}

// evidenceChangedSince is a cheap safety net for a crash between publishing an
// EvidencePackage and invalidating the state row. It compares timestamps only;
// the expensive package load happens only when the state is actually stale.
func (s *Service) evidenceChangedSince(ctx context.Context, knowledgeBase models.KnowledgeBase, at time.Time) (bool, error) {
	var row struct{ UpdatedAt time.Time }
	if err := s.db.WithContext(ctx).Table("evidence_packages").
		Select("updated_at").
		Where("tenant_id = ? AND knowledge_base_id = ? AND status = ?", knowledgeBase.TenantID, knowledgeBase.ID, status.EvidenceReady).
		Order("updated_at DESC").Limit(1).Scan(&row).Error; err != nil {
		return false, err
	}
	if row.UpdatedAt.IsZero() {
		return false, nil
	}
	return row.UpdatedAt.After(at), nil
}

func (s *Service) projectionStateCurrent(state models.KnowledgeProjectionState, knowledgeBase models.KnowledgeBase, strategy retrieval.IndexingStrategy, wikiPrompt, wikiModel, relationPrompt, relationModel string) bool {
	if state.ACLVersion != knowledgeBase.ACLVersion {
		return false
	}
	if strategy.WikiEnabled && (state.WikiExpectedHash == "" || state.WikiPromptVersion != wikiPrompt || state.WikiModel != wikiModel) {
		return false
	}
	if strategy.GraphEnabled && s.relation != nil && (state.RelationExpectedHash == "" || state.RelationPromptVersion != relationPrompt || state.RelationModel != relationModel) {
		return false
	}
	return true
}

func (s *Service) wikiConfiguration() (string, string, error) {
	if s.generator == nil {
		return "", "", nil
	}
	return s.generator.PromptVersion(), s.generator.Model(), nil
}

func (s *Service) loadProjectionState(ctx context.Context, tenantID, knowledgeBaseID string) (models.KnowledgeProjectionState, bool, error) {
	var state models.KnowledgeProjectionState
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	return state, true, nil
}

// upsertWikiExpectedHash records the hash the next wiki run must match. It does
// not touch the relation columns or acl_version, so a concurrent relation
// trigger cannot overwrite them; a stale ACL is detected on the next read.
func (s *Service) upsertWikiExpectedHash(ctx context.Context, knowledgeBase models.KnowledgeBase, hash string) error {
	prompt, model := "", ""
	if s.generator != nil {
		prompt, model = s.generator.PromptVersion(), s.generator.Model()
	}
	state := models.KnowledgeProjectionState{
		TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID, ACLVersion: knowledgeBase.ACLVersion,
		WikiExpectedHash: hash, WikiPromptVersion: prompt, WikiModel: model, UpdatedAt: time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "knowledge_base_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"wiki_expected_hash", "wiki_prompt_version", "wiki_model", "updated_at"}),
	}).Create(&state).Error
}

func (s *Service) upsertRelationExpectedHash(ctx context.Context, knowledgeBase models.KnowledgeBase, hash string) error {
	prompt, model := "", ""
	if s.relation != nil {
		prompt, model = s.relation.PromptVersion(), s.relation.Model()
	}
	state := models.KnowledgeProjectionState{
		TenantID: knowledgeBase.TenantID, KnowledgeBaseID: knowledgeBase.ID, ACLVersion: knowledgeBase.ACLVersion,
		RelationExpectedHash: hash, RelationPromptVersion: prompt, RelationModel: model, UpdatedAt: time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "knowledge_base_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"relation_expected_hash", "relation_prompt_version", "relation_model", "updated_at"}),
	}).Create(&state).Error
}

// InvalidateProjectionState drops the cached hashes so the next gated query
// recomputes them. Pipeline A calls it before triggering projections, which
// keeps a failed trigger from leaving a trusted stale row.
func (s *Service) InvalidateProjectionState(ctx context.Context, tenantID, knowledgeBaseID string) error {
	return s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, knowledgeBaseID).
		Delete(&models.KnowledgeProjectionState{}).Error
}

func (s *Service) latestRun(ctx context.Context, tenantID, knowledgeBaseID, projectionType string) (*models.ProjectionRun, error) {
	var run models.ProjectionRun
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND projection_type = ?", tenantID, knowledgeBaseID, projectionType).
		Order("created_at DESC, id DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}
