package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/circuitbreaker"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/token"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/gorm"
)

// QueryInput is one online query. KnowledgeBaseID is optional: when empty the
// query runs across every knowledge base in the authorization scope.
type QueryInput struct {
	RequestID       string
	Query           string
	KnowledgeBaseID string
	// SessionOverride is the third parameter tier, supplied per request.
	SessionOverride string
	// History is the recent conversation context. The default planner records
	// only its size; a future planner can use it for follow-up rewriting.
	History []HistoryTurn
}

// HistoryTurn is one prior user or assistant message. Only completed answers
// are supplied by the conversation service.
type HistoryTurn struct {
	Role    string
	Content string
}

type Service struct {
	db            *gorm.DB
	keyword       KeywordSearcher
	vector        VectorSearcher
	expander      ChunkExpander
	reranker      Reranker
	rerankModels  RerankModelResolver
	rerankBreaker *circuitbreaker.Breaker
	authz         Authorizer
	join          KnowledgeJoin
	defaults      Params
	tenantParams  TenantParamsResolver
	planner       Planner
	rewriter      Rewriter
	executor      *PlanExecutor
	metrics       metrics.Recorder
}

// TenantParamsResolver loads the tenant tier of the retrieval override chain.
// It is optional: tests and single-tenant fixtures keep the deployment tier.
type TenantParamsResolver interface {
	TenantRetrievalOverride(ctx context.Context, tenantID string) (string, error)
}

type Options struct {
	DB       *gorm.DB
	Keyword  KeywordSearcher
	Vector   VectorSearcher
	Embedder Embedder
	// EmbeddingResolver resolves one vector space per target knowledge base.
	// Nil keeps the single deployment Embedder, which is what unit tests and
	// single-space fixtures use.
	EmbeddingResolver EmbeddingResolver
	Expander          ChunkExpander
	Reranker          Reranker
	// RerankModels resolves the knowledge-base rerank binding. Nil keeps the
	// adapter's configured model.
	RerankModels RerankModelResolver
	// RerankBreaker guards the rerank provider call. Nil uses the default
	// 5-failure / 30s breaker.
	RerankBreaker *circuitbreaker.Breaker
	Authz         Authorizer
	Join          KnowledgeJoin
	Defaults      Params
	// TenantParams resolves the tenant tier. Nil leaves the deployment plus
	// knowledge-base tiers, which is what unit tests and fixtures use.
	TenantParams TenantParamsResolver
	Planner      Planner
	Rewriter     Rewriter
	Metrics      metrics.Recorder
	// Channels and Fusions extend the default keyword/vector + RRF setup. The
	// retrieval service only registers them; the plan chooses what runs.
	Channels []RecallChannel
	Fusions  []FusionStrategy
}

func NewService(options Options) *Service {
	reranker := options.Reranker
	if reranker == nil {
		reranker = NoopReranker{}
	}
	planner := options.Planner
	if planner == nil {
		planner = EvidencePlanner{}
	}
	rewriter := options.Rewriter
	if rewriter == nil {
		rewriter = IdentityRewriter{}
	}
	recorder := options.Metrics
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	breaker := options.RerankBreaker
	if breaker == nil {
		breaker = circuitbreaker.New(circuitbreaker.Config{})
	}
	service := &Service{
		db:            options.DB,
		keyword:       options.Keyword,
		vector:        options.Vector,
		expander:      options.Expander,
		reranker:      reranker,
		rerankModels:  options.RerankModels,
		rerankBreaker: breaker,
		authz:         options.Authz,
		join:          options.Join,
		defaults:      options.Defaults,
		tenantParams:  options.TenantParams,
		planner:       planner,
		rewriter:      rewriter,
		metrics:       recorder,
	}
	channels := append([]RecallChannel{}, options.Channels...)
	if options.Keyword != nil {
		channels = append(channels, KeywordChannel{Searcher: options.Keyword})
	}
	if options.Vector != nil && (options.EmbeddingResolver != nil || options.Embedder != nil) {
		channels = append(channels, VectorChannel{
			Searcher: options.Vector, Embedder: options.Embedder, Resolver: options.EmbeddingResolver, Metrics: service.metrics,
		})
	}
	service.executor = NewPlanExecutor(channels, options.Fusions)
	return service
}

// Retrieve runs the fixed first-phase plan: normalize, resolve parameters,
// recall from both channels in parallel, fuse, expand, trim and re-check
// authorization.
func (s *Service) Retrieve(ctx context.Context, scope authz.AuthorizationScope, input QueryInput) (EvidenceSet, error) {
	started := time.Now()
	defer func() { s.metrics.Observe("retrieval_duration_ms", time.Since(started), nil) }()
	ctx, span := observability.StartSpan(ctx, "retrieval.retrieve",
		attribute.String("request.id", input.RequestID),
		attribute.String("tenant.id", scope.TenantID),
	)
	defer span.End()
	query := normalizeQuery(input.Query)
	if query == "" {
		return EvidenceSet{}, apperror.Invalidf("query is required")
	}
	if strings.TrimSpace(scope.TenantID) == "" {
		return EvidenceSet{}, apperror.Invalidf("authorization scope is required")
	}
	retrievalQuery := query
	if len(input.History) > 0 {
		rewritten, err := s.rewriter.Rewrite(ctx, query, input.History)
		switch {
		case err != nil:
			span.SetAttributes(attribute.String("query.rewrite_error", err.Error()))
		case strings.TrimSpace(rewritten) != "":
			retrievalQuery = normalizeQuery(rewritten)
			span.SetAttributes(attribute.Bool("query.rewrite_applied", retrievalQuery != query))
		}
	}

	targetIDs := scope.KnowledgeBaseIDs
	if input.KnowledgeBaseID != "" {
		if !contains(targetIDs, input.KnowledgeBaseID) {
			return EvidenceSet{}, apperror.ErrForbidden
		}
		targetIDs = []string{input.KnowledgeBaseID}
	}
	if len(targetIDs) == 0 {
		return EvidenceSet{}, nil
	}

	strategy, params, err := s.resolveConfig(ctx, scope, input)
	if err != nil {
		return EvidenceSet{}, err
	}
	if err := s.enforceCapabilities(ctx, scope, targetIDs, strategy, input.KnowledgeBaseID); err != nil {
		return EvidenceSet{}, err
	}

	recall := RecallRequest{TenantID: scope.TenantID, KnowledgeBaseIDs: targetIDs, Query: retrievalQuery}
	planQuery := input
	planQuery.Query = retrievalQuery
	plan, err := s.planner.Plan(ctx, PlanInput{
		Scope: scope, Query: planQuery, TargetKnowledgeBaseIDs: targetIDs,
		Strategy: strategy, Params: params, History: input.History,
	})
	if err != nil {
		return EvidenceSet{}, fmt.Errorf("build query plan: %w", err)
	}

	candidates, err := s.executor.Execute(ctx, plan, params, recall)
	if err != nil {
		return EvidenceSet{}, err
	}
	deduped := dedupe(candidates)

	rerankApplied := false
	if plan.RerankEnabled && s.reranker != nil && s.reranker.Name() != "none" {
		// The binding only applies to a single knowledge base; a cross-KB query
		// has no one binding and keeps the adapter's configured model. Offline
		// rerankers ignore the model, so resolving would spend the tenant model
		// budget on a provider call that never happens.
		_, perRequestModel := s.reranker.(ModelReranker)
		rerankModel := ""
		degradeReason := ""
		if perRequestModel && s.rerankModels != nil && input.KnowledgeBaseID != "" {
			resolved, err := s.rerankModels.RerankModel(ctx, scope.TenantID, input.KnowledgeBaseID)
			if err != nil {
				degradeReason = "resolve"
				s.metrics.Inc("model_errors_total", metrics.Labels("model", "rerank", "stage", "resolve"))
				slog.Warn("rerank_degraded", "reason", degradeReason, "knowledge_base_id", input.KnowledgeBaseID, "error", err)
			} else {
				rerankModel = resolved
			}
		}
		// Rerank never fails the query: recall and fusion already produced a
		// usable evidence set, so an outage only loses the reordering.
		if degradeReason == "" && !s.rerankBreaker.Allow() {
			degradeReason = "breaker_open"
			slog.Warn("rerank_degraded", "reason", degradeReason, "knowledge_base_id", input.KnowledgeBaseID)
		}
		if degradeReason == "" {
			reranked, err := s.rerank(ctx, rerankModel, query, deduped)
			if err != nil {
				modelLabel := rerankModel
				if modelLabel == "" {
					modelLabel = s.reranker.Name()
				}
				degradeReason = "error"
				s.metrics.Inc("model_errors_total", metrics.Labels("model", modelLabel, "stage", "rerank"))
				if ctx.Err() != nil {
					s.rerankBreaker.Release()
				} else {
					s.rerankBreaker.Failure()
				}
				slog.Warn("rerank_degraded", "reason", degradeReason, "model", modelLabel, "knowledge_base_id", input.KnowledgeBaseID, "error", err)
			} else {
				s.rerankBreaker.Success()
				// rerank_threshold and rerank_top_k bound the reranked candidate set that
				// goes on to context expansion. They only apply while rerank is actually
				// running; top_k still bounds the final evidence list below.
				reranked = aboveRerankThreshold(reranked, params.RerankThreshold)
				reranked = truncateReranked(reranked, params.RerankTopK)
				deduped = reranked
				rerankApplied = true
			}
		}
		if degradeReason != "" {
			s.metrics.Inc("rerank_degraded_total", metrics.Labels("reason", degradeReason))
			span.SetAttributes(attribute.String("rerank.degraded_reason", degradeReason))
		}
	}

	expanded := deduped
	// Parent chunks are always completed when an expander is wired: the two
	// counters govern sibling neighbours only, so zeroing them must not drop the
	// parent context as well.
	if s.expander != nil {
		expanded, err = s.expander.Expand(ctx, recall, deduped, ExpansionOptions{
			PerSide:     params.AdjacentExpansion,
			MaxTotal:    params.MaxAdjacentChunks,
			ScoreFactor: params.ExpansionScoreFactor,
		})
		if err != nil {
			return EvidenceSet{}, fmt.Errorf("expand evidence context: %w", err)
		}
	}
	expanded = dedupe(expanded)
	SortCandidates(expanded)

	if plan.TopK >= 0 && len(expanded) > plan.TopK {
		expanded = expanded[:plan.TopK]
	}
	expanded, tokens := applyTokenLimit(expanded, params.ContextTokenLimit)

	authorized, dropped, err := s.recheckAuthorization(ctx, scope, expanded)
	if err != nil {
		return EvidenceSet{}, err
	}

	span.SetAttributes(
		attribute.Int("evidence.count", len(authorized)),
		attribute.Int("evidence.dropped_by_authorization", dropped),
		attribute.Bool("rerank.applied", rerankApplied),
		attribute.Int("context.token_estimate", tokens),
	)
	return EvidenceSet{
		SchemaVersion:             SchemaEvidenceSet,
		RequestID:                 input.RequestID,
		AuthorizationScopeVersion: scope.ACLVersion,
		Query:                     query,
		Plan:                      plan,
		Items:                     authorized,
		TokenEstimate:             tokens,
		RecallLatencyMS:           time.Since(started).Milliseconds(),
		RerankApplied:             rerankApplied,
		DroppedByAuthorization:    dropped,
	}, nil
}

// rerank applies the resolved model when the adapter supports per-request model
// selection; otherwise it keeps the adapter's configured behavior.
func (s *Service) rerank(ctx context.Context, model, query string, candidates []Candidate) ([]Candidate, error) {
	if model != "" {
		if modelReranker, ok := s.reranker.(ModelReranker); ok {
			return modelReranker.RerankWithModel(ctx, model, query, candidates)
		}
	}
	return s.reranker.Rerank(ctx, query, candidates)
}

// fuseFRL keeps the two-channel call shape for callers that do not build a
// plan. It delegates to the default RRF fusion strategy.
func fuseFRL(vectorHits, keywordHits []Hit, rrfK int) []Candidate {
	return RRFusion{}.Fuse(map[string][]Hit{
		ChannelVector:  vectorHits,
		ChannelKeyword: keywordHits,
	}, Params{RRFK: rrfK})
}

func dedupe(candidates []Candidate) []Candidate {
	seen := map[string]bool{}
	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ChunkID == "" || seen[candidate.ChunkID] {
			continue
		}
		seen[candidate.ChunkID] = true
		result = append(result, candidate)
	}
	return result
}

// aboveRerankThreshold drops candidates the reranker scored below the floor.
// Candidates without a rerank score are kept: only the reranker's own verdict
// can exclude a candidate, not the absence of one.
func aboveRerankThreshold(candidates []Candidate, threshold float64) []Candidate {
	kept := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.RerankScore != nil && *candidate.RerankScore < threshold {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

// truncateReranked keeps the highest ranked rerank_top_k candidates. A limit of
// 0 or less means "do not truncate", so the default cannot silently narrow the
// candidate set.
func truncateReranked(candidates []Candidate, limit int) []Candidate {
	if limit <= 0 || len(candidates) <= limit {
		return candidates
	}
	return candidates[:limit]
}

// SortCandidates orders candidates by the single ranking key: a reranker's
// score when it has one, the fused score otherwise. Reranker adapters outside
// this package call it so their output ordering matches the service's, and the
// service re-applies it after expansion instead of letting the final sort undo
// the reranker's decision.
func SortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if left, right := orderScore(candidates[i]), orderScore(candidates[j]); left != right {
			return left > right
		}
		return candidates[i].ChunkID < candidates[j].ChunkID
	})
}

// applyTokenLimit trims the tail of the ranked evidence list until the
// assembled context fits the configured budget. Trimming from the tail keeps the
// highest ranked evidence, which is what generation needs most.
func applyTokenLimit(candidates []Candidate, limit int) ([]Candidate, int) {
	if limit <= 0 {
		return candidates, estimateTokens(candidates)
	}
	used := 0
	kept := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		cost := token.Estimate(candidate.Content) + token.Estimate(candidate.HeadingPath)
		if used+cost > limit && len(kept) > 0 {
			break
		}
		used += cost
		kept = append(kept, candidate)
	}
	return kept, used
}

// estimateTokens is an approximation: one token per two runes. It is documented
// as approximate so the limit is a safety bound, not an exact model count.
func estimateTokens(candidates []Candidate) int {
	total := 0
	for _, candidate := range candidates {
		total += token.Estimate(candidate.Content) + token.Estimate(candidate.HeadingPath)
	}
	return total
}

// recheckAuthorization filters the final evidence set once more. A scope or ACL
// version can change between recall and delivery.
func (s *Service) recheckAuthorization(ctx context.Context, scope authz.AuthorizationScope, candidates []Candidate) ([]Candidate, int, error) {
	if s.authz == nil || len(candidates) == 0 {
		return candidates, 0, nil
	}
	refs := make([]authz.ResourceRef, 0, len(candidates))
	for _, candidate := range candidates {
		refs = append(refs, authz.ResourceRef{TenantID: scope.TenantID, KnowledgeBaseID: candidate.KnowledgeBaseID, ResourceID: candidate.ChunkID, ResourceType: "chunk"})
	}
	readable, err := s.authz.FilterReadable(ctx, scope, refs)
	if err != nil {
		return nil, 0, err
	}
	allowed := map[string]bool{}
	for _, ref := range readable {
		allowed[ref.ResourceID] = true
	}
	kept := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if allowed[candidate.ChunkID] {
			kept = append(kept, candidate)
		}
	}
	return kept, len(candidates) - len(kept), nil
}

func (s *Service) resolveConfig(ctx context.Context, scope authz.AuthorizationScope, input QueryInput) (IndexingStrategy, Params, error) {
	var knowledgeBase models.KnowledgeBase
	strategy := DefaultIndexingStrategy()
	knowledgeBaseRaw := ""
	if input.KnowledgeBaseID != "" && s.db != nil {
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", scope.TenantID, input.KnowledgeBaseID).First(&knowledgeBase).Error; err != nil {
			return IndexingStrategy{}, Params{}, fmt.Errorf("load knowledge base retrieval config: %w", err)
		}
		knowledgeBaseRaw = knowledgeBase.RetrievalConfig
		parsed, err := ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
		if err != nil {
			return IndexingStrategy{}, Params{}, err
		}
		strategy = parsed
	}
	tenantRaw := ""
	if s.tenantParams != nil {
		raw, err := s.tenantParams.TenantRetrievalOverride(ctx, scope.TenantID)
		if err != nil {
			return IndexingStrategy{}, Params{}, err
		}
		tenantRaw = raw
	}
	params, err := ResolveParamsWithTenant(s.defaults, tenantRaw, knowledgeBaseRaw, input.SessionOverride)
	if err != nil {
		return IndexingStrategy{}, Params{}, err
	}
	return strategy, params, nil
}

func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
