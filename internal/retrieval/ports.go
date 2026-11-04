package retrieval

import (
	"context"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/embedding"
)

// RecallRequest is the scope-bearing recall query. Authorization filters are
// part of the request, not a post-processing step, so a recall query can never
// return rows outside the caller's scope.
type RecallRequest struct {
	TenantID         string
	KnowledgeBaseIDs []string
	Query            string
	// EmbeddingModel is the model that produced the query vector. Vector recall
	// filters on it so a chunk embedded by another model is never compared.
	EmbeddingModel string
	TopK           int
	Threshold      float64
}

// Rewriter turns a follow-up question into a standalone retrieval query using
// prior conversation turns. An error or empty result means the caller keeps the
// original question.
type Rewriter interface {
	Rewrite(ctx context.Context, query string, history []HistoryTurn) (string, error)
}

// IdentityRewriter keeps the original query. It is the default so retrieval
// never depends on a model unless a rewriter is explicitly wired.
type IdentityRewriter struct{}

func (IdentityRewriter) Rewrite(_ context.Context, query string, _ []HistoryTurn) (string, error) {
	return query, nil
}

// Planner turns a resolved query into an executable plan. The default planner
// builds the evidence-only plan; a future planner can add Wiki/relation steps
// without changing the recall and authorization stages.
type Planner interface {
	Plan(ctx context.Context, input PlanInput) (QueryPlan, error)
}

// PlanInput is everything the planner may consider. Strategy and Params are
// already resolved by the caller, so the planner does not re-read configuration.
type PlanInput struct {
	Scope                  authz.AuthorizationScope
	Query                  QueryInput
	TargetKnowledgeBaseIDs []string
	Strategy               IndexingStrategy
	Params                 Params
	History                []HistoryTurn
}

// KeywordSearcher performs BM25 recall over the Evidence Index.
type KeywordSearcher interface {
	SearchKeyword(ctx context.Context, request RecallRequest) ([]Hit, error)
}

// VectorSearcher performs cosine recall over the Evidence Index.
type VectorSearcher interface {
	SearchVector(ctx context.Context, request RecallRequest, embedding []float32) ([]Hit, error)
}

// ExpansionOptions is the resolved expansion policy for one query. Parents are
// always completed when an expander is wired; the counters bound sibling
// neighbours only.
type ExpansionOptions struct {
	// PerSide is how many sibling neighbours are added on each side of a
	// selected chunk.
	PerSide int
	// MaxTotal caps how many neighbour chunks may be added in total, across all
	// documents. Zero or less disables neighbours entirely, which is the
	// explicit way to keep parent completion while turning sibling expansion off.
	MaxTotal int
	// ScoreFactor discounts the anchor score an expanded chunk inherits, so
	// expanded context never outranks the chunk that pulled it in.
	ScoreFactor float64
}

// ChunkExpander loads parent chunks and sibling neighbours for context
// completion. Implementations must apply the same scope filtering as recall.
type ChunkExpander interface {
	Expand(ctx context.Context, request RecallRequest, selected []Candidate, options ExpansionOptions) ([]Candidate, error)
}

// Reranker reorders candidates. The default implementation is a pass-through so
// rerank stays a configurable capability rather than a required dependency.
type Reranker interface {
	Rerank(ctx context.Context, query string, candidates []Candidate) ([]Candidate, error)
	Name() string
}

// Embedder is the shared model port: ingestion writes and retrieval queries
// through the same contract, so the configured model cannot drift between them.
type Embedder = embedding.Embedder

// Authorizer re-checks the final evidence set against the authorization scope.
// A scope can change between recall and delivery, so the evidence set is
// filtered once more before it is handed to generation.
type Authorizer interface {
	FilterReadable(ctx context.Context, scope authz.AuthorizationScope, refs []authz.ResourceRef) ([]authz.ResourceRef, error)
}

// CapabilityStatus reports each projection's readiness independently. A query
// is gated only by the capabilities it declares, so a failed relation run never
// fails a wiki-only or evidence-only query. A disabled projection is vacuously
// ready; the aggregate join status is reported elsewhere for display.
type CapabilityStatus struct {
	WikiReady     bool
	RelationReady bool
}

// KnowledgeJoin reports per-projection readiness for the ProjectionJoin gate.
type KnowledgeJoin interface {
	CapabilityStatus(ctx context.Context, tenantID, knowledgeBaseID string) (CapabilityStatus, error)
}
