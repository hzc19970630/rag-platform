package retrieval

import (
	"context"
	"fmt"
	"strings"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)

const (
	CapabilityEvidence = "evidence"
	CapabilityWiki     = "wiki"
	CapabilityRelation = "relation"
	CapabilityGraph    = "graph"

	JoinKnowledgeReady = "KNOWLEDGE_READY"
)

// ValidateIndexingStrategy rejects capability combinations the running
// milestone cannot honour. Enabling graph projection is never required: a
// knowledge base may project relations without making every query use them.
func ValidateIndexingStrategy(strategy IndexingStrategy) error {
	for _, capability := range strategy.RequiredCapabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case CapabilityEvidence:
		case CapabilityWiki:
			if !strategy.WikiEnabled {
				return apperror.Invalidf("required capability wiki must enable wiki_enabled")
			}
		case CapabilityRelation, CapabilityGraph:
			if !strategy.GraphEnabled {
				return apperror.Invalidf("required capability %q must enable graph_enabled", capability)
			}
		case "":
		default:
			return apperror.Invalidf("unsupported required capability %q", capability)
		}
	}
	return nil
}

func requiresCapability(strategy IndexingStrategy, expected string) bool {
	for _, capability := range strategy.RequiredCapabilities {
		if strings.EqualFold(strings.TrimSpace(capability), expected) {
			return true
		}
	}
	return false
}

// enforceCapabilities implements the ProjectionJoin gate for the query plan.
// Each declared capability is checked against its own projection: a query that
// requires wiki needs a current wiki run even when the relation run failed, and
// a query that requires relation or graph needs a current relation run. A query
// that declares neither never consults the projections.
func (s *Service) enforceCapabilities(ctx context.Context, scope authz.AuthorizationScope, targetIDs []string, single IndexingStrategy, singleID string) error {
	strategies := map[string]IndexingStrategy{}
	if singleID != "" {
		strategies[singleID] = single
	} else if s.db != nil {
		var rows []models.KnowledgeBase
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id IN ?", scope.TenantID, targetIDs).Find(&rows).Error; err != nil {
			return fmt.Errorf("load indexing strategies: %w", err)
		}
		for _, row := range rows {
			strategy, err := ParseIndexingStrategy(row.IndexingStrategy)
			if err != nil {
				return err
			}
			strategies[row.ID] = strategy
		}
	}
	if len(strategies) == 0 {
		for _, id := range targetIDs {
			strategies[id] = single
		}
	}
	for knowledgeBaseID, strategy := range strategies {
		if err := ValidateIndexingStrategy(strategy); err != nil {
			return fmt.Errorf("%w: knowledge base %s: %v", apperror.ErrNotReady, knowledgeBaseID, err)
		}
		needsWiki := requiresCapability(strategy, CapabilityWiki)
		needsRelation := requiresCapability(strategy, CapabilityRelation) || requiresCapability(strategy, CapabilityGraph)
		if !needsWiki && !needsRelation {
			continue
		}
		if s.join == nil {
			return fmt.Errorf("%w: knowledge projection join is not configured", apperror.ErrNotReady)
		}
		status, err := s.join.CapabilityStatus(ctx, scope.TenantID, knowledgeBaseID)
		if err != nil {
			return err
		}
		if needsWiki && !status.WikiReady {
			return fmt.Errorf("%w: wiki projection is not ready for knowledge base %s", apperror.ErrNotReady, knowledgeBaseID)
		}
		if needsRelation && !status.RelationReady {
			return fmt.Errorf("%w: relation projection is not ready for knowledge base %s", apperror.ErrNotReady, knowledgeBaseID)
		}
	}
	return nil
}
