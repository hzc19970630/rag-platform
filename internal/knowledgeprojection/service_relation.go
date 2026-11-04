package knowledgeprojection

import (
	"context"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
)

func (s *Service) graphCounts(ctx context.Context, tenantID, knowledgeBaseID string) (GraphCounts, error) {
	if s.store == nil {
		return GraphCounts{}, nil
	}
	counts, err := s.store.Counts(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return GraphCounts{}, err
	}
	return GraphCounts{
		Snapshots: counts.Snapshots, ActiveSnapshot: counts.ActiveSnapshot,
		Entities: counts.Entities, Relations: counts.Relations, PendingReview: counts.PendingReview,
	}, nil
}

// RebuildGraph is the operator entry point for relation re-projection. Like the
// wiki rebuild it never publishes: the resulting snapshot needs review.
func (s *Service) RebuildGraph(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (int, error) {
	if principal.CredentialType == "api_key" {
		return 0, apperror.ErrForbidden
	}
	if _, err := s.authorize(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return 0, err
	}
	count, err := s.RebuildRelations(ctx, principal.TenantID, knowledgeBaseID)
	if err != nil {
		return 0, err
	}
	s.recordAuditResource(ctx, principal, knowledgeBaseID, "graph_snapshot", knowledgeBaseID, "graph_rebuild")
	return count, nil
}
