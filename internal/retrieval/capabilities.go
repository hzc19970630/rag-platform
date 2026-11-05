package retrieval

import (
	"context"
	"fmt"
	"sort"
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

	projectionWiki     = "wiki"
	projectionRelation = "relation"
)

// CapabilitySpec is one entry of the capability registry. Validation rule,
// projection gate and status lookup live here, so adding a capability changes
// this registry rather than the validator and the query gate separately.
type CapabilitySpec struct {
	Name     string
	Validate func(IndexingStrategy) error
	// Projection names the readiness gate a query must pass; empty means the
	// capability is served by the evidence index directly.
	Projection string
}

// projectionGate resolves one projection readiness flag. It is a registry too,
// so a capability only records which gate it needs.
type projectionGate struct {
	Label string
	Ready func(CapabilityStatus) bool
}

var projectionGates = map[string]projectionGate{
	projectionWiki:     {Label: "wiki", Ready: func(status CapabilityStatus) bool { return status.WikiReady }},
	projectionRelation: {Label: "relation", Ready: func(status CapabilityStatus) bool { return status.RelationReady }},
}

var capabilityRegistry = map[string]CapabilitySpec{
	CapabilityEvidence: {
		Name:     CapabilityEvidence,
		Validate: func(IndexingStrategy) error { return nil },
	},
	CapabilityWiki: {
		Name: CapabilityWiki,
		Validate: func(strategy IndexingStrategy) error {
			if !strategy.WikiEnabled {
				return fmt.Errorf("required capability %s must enable wiki_enabled", CapabilityWiki)
			}
			return nil
		},
		Projection: projectionWiki,
	},
	CapabilityRelation: {
		Name: CapabilityRelation,
		Validate: func(strategy IndexingStrategy) error {
			if !strategy.GraphEnabled {
				return fmt.Errorf("required capability %s must enable graph_enabled", CapabilityRelation)
			}
			return nil
		},
		Projection: projectionRelation,
	},
	CapabilityGraph: {
		Name: CapabilityGraph,
		Validate: func(strategy IndexingStrategy) error {
			if !strategy.GraphEnabled {
				return fmt.Errorf("required capability %s must enable graph_enabled", CapabilityGraph)
			}
			return nil
		},
		Projection: projectionRelation,
	},
}

// LookupCapability returns the registered capability. Names are matched after
// trim + lowercase, which is the form stored by the knowledge base.
func LookupCapability(name string) (CapabilitySpec, bool) {
	spec, ok := capabilityRegistry[strings.ToLower(strings.TrimSpace(name))]
	return spec, ok
}

// CapabilityNames is the enumerable capability catalog, sorted.
func CapabilityNames() []string {
	names := make([]string, 0, len(capabilityRegistry))
	for name := range capabilityRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateIndexingStrategy rejects capability combinations the running
// milestone cannot honour. Enabling graph projection is never required: a
// knowledge base may project relations without making every query use them.
func ValidateIndexingStrategy(strategy IndexingStrategy) error {
	for _, capability := range strategy.RequiredCapabilities {
		trimmed := strings.TrimSpace(capability)
		if trimmed == "" {
			continue
		}
		spec, ok := LookupCapability(trimmed)
		if !ok {
			return apperror.Invalidf("unsupported required capability %q", capability)
		}
		if err := spec.Validate(strategy); err != nil {
			return apperror.Invalidf("%v", err)
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

// requiredProjectionGates resolves the capabilities a strategy declares to the
// projections that must be ready. Unknown names are reported as not ready, so a
// missing registry entry cannot silently downgrade a query's gate.
func requiredProjectionGates(strategy IndexingStrategy) (map[string]bool, error) {
	gates := map[string]bool{}
	for _, capability := range strategy.RequiredCapabilities {
		trimmed := strings.TrimSpace(capability)
		if trimmed == "" {
			continue
		}
		spec, ok := LookupCapability(trimmed)
		if !ok {
			return nil, fmt.Errorf("unsupported required capability %q", capability)
		}
		if spec.Projection != "" {
			gates[spec.Projection] = true
		}
	}
	return gates, nil
}

// enforceCapabilities implements the ProjectionJoin gate for the query plan.
// Each declared capability is checked against its own projection: a query that
// requires wiki needs a current wiki run even when the relation run failed, and
// a query that requires relation or graph needs a current relation run. A query
// that declares no projection capability never consults the projections.
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
		gates, err := requiredProjectionGates(strategy)
		if err != nil {
			return fmt.Errorf("%w: knowledge base %s: %v", apperror.ErrNotReady, knowledgeBaseID, err)
		}
		if len(gates) == 0 {
			continue
		}
		if s.join == nil {
			return fmt.Errorf("%w: knowledge projection join is not configured", apperror.ErrNotReady)
		}
		status, err := s.join.CapabilityStatus(ctx, scope.TenantID, knowledgeBaseID)
		if err != nil {
			return err
		}
		for projection := range gates {
			gate, ok := projectionGates[projection]
			if !ok || !gate.Ready(status) {
				return fmt.Errorf("%w: %s projection is not ready for knowledge base %s", apperror.ErrNotReady, projection, knowledgeBaseID)
			}
		}
	}
	return nil
}
