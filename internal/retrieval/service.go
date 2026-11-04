package retrieval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/token"
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
	db       *gorm.DB
	keyword  KeywordSearcher
	vector   VectorSearcher
	embedder Embedder
	expander ChunkExpander
	reranker Reranker
	authz    Authorizer
	join     KnowledgeJoin
	defaults Params
	planner  Planner
	rewriter Rewriter
}

type Options struct {
	DB       *gorm.DB
	Keyword  KeywordSearcher
	Vector   VectorSearcher
	Embedder Embedder
	Expander ChunkExpander
	Reranker Reranker
	Authz    Authorizer
	Join     KnowledgeJoin
	Defaults Params
	Planner  Planner
	Rewriter Rewriter
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
	return &Service{
		db:       options.DB,
		keyword:  options.Keyword,
		vector:   options.Vector,
		embedder: options.Embedder,
		expander: options.Expander,
		reranker: reranker,
		authz:    options.Authz,
		join:     options.Join,
		defaults: options.Defaults,
		planner:  planner,
		rewriter: rewriter,
	}
}

// Retrieve runs the fixed first-phase plan: normalize, resolve parameters,
// recall from both channels in parallel, fuse, expand, trim and re-check
// authorization.
func (s *Service) Retrieve(ctx context.Context, scope authz.AuthorizationScope, input QueryInput) (EvidenceSet, error) {
	started := time.Now()
	defer func() { observability.Default().Observe("retrieval_duration_ms", time.Since(started), nil) }()
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

	vectorHits, keywordHits, err := s.recall(ctx, plan, params, recall)
	if err != nil {
		return EvidenceSet{}, err
	}

	candidates := fuseFRL(vectorHits, keywordHits, params.RRFK)
	deduped := dedupe(candidates)

	rerankApplied := false
	if params.RerankEnabled && s.reranker != nil && s.reranker.Name() != "none" {
		reranked, err := s.reranker.Rerank(ctx, query, deduped)
		if err != nil {
			observability.Default().Inc("model_errors_total", observability.Labels("model", "rerank", "stage", "rerank"))
			return EvidenceSet{}, fmt.Errorf("rerank: %w", err)
		}
		// rerank_threshold and rerank_top_k bound the reranked candidate set that
		// goes on to context expansion. They only apply while rerank is actually
		// running; top_k still bounds the final evidence list below.
		reranked = aboveRerankThreshold(reranked, params.RerankThreshold)
		reranked = truncateReranked(reranked, params.RerankTopK)
		deduped = reranked
		rerankApplied = true
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

	if params.TopK >= 0 && len(expanded) > params.TopK {
		expanded = expanded[:params.TopK]
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

// recall runs both channels concurrently. A single channel failing does not
// fail the query, because the other channel may still ground the answer; both
// failing does.
func (s *Service) recall(ctx context.Context, plan QueryPlan, params Params, request RecallRequest) ([]Hit, []Hit, error) {
	var (
		wg          sync.WaitGroup
		vectorHits  []Hit
		keywordHits []Hit
		vectorErr   error
		keywordErr  error
		attempted   int
	)

	if plan.KeywordEnabled && s.keyword != nil {
		attempted++
		keywordRequest := request
		keywordRequest.TopK = params.KeywordTopK
		keywordRequest.Threshold = params.KeywordThreshold
		wg.Add(1)
		go func() {
			defer wg.Done()
			keywordHits, keywordErr = s.keyword.SearchKeyword(ctx, keywordRequest)
		}()
	}
	if plan.VectorEnabled && s.vector != nil && s.embedder != nil {
		attempted++
		vectorRequest := request
		vectorRequest.EmbeddingModel = s.embedder.Name()
		vectorRequest.TopK = params.EmbeddingTopK
		vectorRequest.Threshold = params.VectorThreshold
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.verifyEmbeddingBinding(ctx, request.TenantID, request.KnowledgeBaseIDs); err != nil {
				vectorErr = fmt.Errorf("embedding binding mismatch: %w", err)
				return
			}
			vectors, err := s.embedder.Embed(ctx, []string{request.Query})
			if err != nil {
				observability.Default().Inc("model_errors_total", observability.Labels("model", "embedding", "stage", "query"))
				vectorErr = fmt.Errorf("embed query: %w", err)
				return
			}
			if len(vectors) != 1 {
				vectorErr = fmt.Errorf("embed query returned %d vectors", len(vectors))
				return
			}
			if len(vectors[0]) != s.embedder.Dimension() {
				vectorErr = fmt.Errorf("query vector dimension %d does not match configured dimension %d", len(vectors[0]), s.embedder.Dimension())
				return
			}
			vectorHits, vectorErr = s.vector.SearchVector(ctx, vectorRequest, vectors[0])
		}()
	}
	wg.Wait()

	if attempted == 0 {
		return nil, nil, fmt.Errorf("no retrieval channel is enabled for this knowledge base")
	}
	if keywordErr != nil && vectorErr != nil {
		return nil, nil, fmt.Errorf("all retrieval channels failed: keyword: %v; vector: %v", keywordErr, vectorErr)
	}
	if keywordErr != nil {
		keywordHits = nil
	}
	if vectorErr != nil {
		vectorHits = nil
	}
	return vectorHits, keywordHits, nil
}

// verifyEmbeddingBinding refuses to embed a query for a knowledge base whose
// vectors come from another model or dimension. The vector channel fails while
// keyword recall can still serve the request; an empty binding is a pre-upgrade
// row and is treated as unbounded.
func (s *Service) verifyEmbeddingBinding(ctx context.Context, tenantID string, knowledgeBaseIDs []string) error {
	if s.db == nil || s.embedder == nil {
		return nil
	}
	var rows []models.KnowledgeBase
	if err := s.db.WithContext(ctx).
		Select("id", "embedding_model", "embedding_dimension").
		Where("tenant_id = ? AND id IN ?", tenantID, knowledgeBaseIDs).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("load knowledge base embedding bindings: %w", err)
	}
	for _, row := range rows {
		if row.EmbeddingModel == "" {
			continue
		}
		if row.EmbeddingModel != s.embedder.Name() || row.EmbeddingDimension != s.embedder.Dimension() {
			return fmt.Errorf("knowledge base %s is bound to %s/%d, this deployment serves %s/%d", row.ID, row.EmbeddingModel, row.EmbeddingDimension, s.embedder.Name(), s.embedder.Dimension())
		}
	}
	return nil
}

// fuseFRL merges the two ranked lists with reciprocal rank fusion. RRF is used
// because BM25 and cosine scores are not comparable. The damping constant comes
// from the resolved retrieval parameters (rrf_k), so the score band a fused
// result lands in is policy rather than a code constant.
func fuseFRL(vectorHits, keywordHits []Hit, rrfK int) []Candidate {
	byID := map[string]*Candidate{}
	merge := func(hits []Hit, channel string, isVector bool) {
		for index, hit := range hits {
			rank := hit.Rank
			if rank <= 0 {
				rank = index + 1
			}
			candidate, exists := byID[hit.ChunkID]
			if !exists {
				candidate = &Candidate{
					ChunkID:           hit.ChunkID,
					DocumentID:        hit.DocumentID,
					DocumentVersionID: hit.DocumentVersionID,
					KnowledgeBaseID:   hit.KnowledgeBaseID,
					ParentID:          hit.ParentID,
					Ordinal:           hit.Ordinal,
					Content:           hit.Content,
					ContentHash:       hit.ContentHash,
					HeadingPath:       hit.HeadingPath,
					PageNumber:        hit.PageNumber,
					SourcePath:        hit.SourcePath,
				}
				byID[hit.ChunkID] = candidate
			}
			candidate.Score += 1.0 / float64(rrfK+rank)
			if isVector {
				candidate.VectorRank = rank
				candidate.VectorScore = hit.Score
			} else {
				candidate.KeywordRank = rank
				candidate.KeywordScore = hit.Score
			}
			if !contains(candidate.Channels, channel) {
				candidate.Channels = append(candidate.Channels, channel)
			}
		}
	}
	merge(vectorHits, ChannelVector, true)
	merge(keywordHits, ChannelKeyword, false)

	result := make([]Candidate, 0, len(byID))
	for _, candidate := range byID {
		if candidate.VectorRank > 0 && candidate.KeywordRank > 0 {
			candidate.Channels = []string{ChannelBoth}
		}
		result = append(result, *candidate)
	}
	SortCandidates(result)
	return result
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
	params, err := ResolveParams(s.defaults, knowledgeBaseRaw, input.SessionOverride)
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
