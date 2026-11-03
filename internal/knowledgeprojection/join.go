package knowledgeprojection

import (
	"context"
	"errors"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
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
// reported ready so callers can gate only declared capabilities.
func (s *Service) evaluateJoin(ctx context.Context, knowledgeBase models.KnowledgeBase, strategy retrieval.IndexingStrategy) (joinEvaluation, error) {
	if !strategy.WikiEnabled && !strategy.GraphEnabled {
		return joinEvaluation{status: JoinEvidenceReady, wikiReady: true, relationReady: true}, nil
	}
	packages, err := s.loadCurrentPackages(ctx, knowledgeBase.TenantID, knowledgeBase.ID)
	if err != nil {
		return joinEvaluation{}, err
	}
	wikiHash, relationHash := "", ""
	if len(packages) > 0 {
		wikiHash, _ = packagesSourceHash(packages, knowledgeBase.ACLVersion, s.generator.PromptVersion(), s.generator.Model())
		if s.relation != nil {
			relationHash = s.relationSourceHash(packages, knowledgeBase.ACLVersion)
		}
	}
	evaluation := joinEvaluation{wikiReady: true, relationReady: true}
	var latestWiki, latestRelation *models.ProjectionRun
	if strategy.WikiEnabled {
		latestWiki, err = s.latestRun(ctx, knowledgeBase.ID, ProjectionTypeWiki)
		if err != nil {
			return joinEvaluation{}, err
		}
		evaluation.wikiReady = projectionCurrent(latestWiki, wikiHash)
	}
	if strategy.GraphEnabled {
		if s.relation != nil {
			latestRelation, err = s.latestRun(ctx, knowledgeBase.ID, ProjectionTypeRelation)
			if err != nil {
				return joinEvaluation{}, err
			}
		}
		evaluation.relationReady = projectionCurrent(latestRelation, relationHash)
	}
	evaluation.status = ComputeJoin(strategy.WikiEnabled, strategy.GraphEnabled, latestWiki, latestRelation, wikiHash, relationHash)
	return evaluation, nil
}

func (s *Service) latestRun(ctx context.Context, knowledgeBaseID, projectionType string) (*models.ProjectionRun, error) {
	var run models.ProjectionRun
	err := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND projection_type = ?", knowledgeBaseID, projectionType).
		Order("created_at DESC, id DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}
