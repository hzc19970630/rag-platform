package retrieval

import (
	"context"
	"fmt"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"sort"
	"strings"
	"sync"
)

const (
	FusionRRF = "rrf"
)

// RecallChannel is one independently executable recall path. A channel owns its
// own query shaping (top-k, threshold, embedding) so adding a channel does not
// change the executor or the retrieval service.
type RecallChannel interface {
	Name() string
	Recall(ctx context.Context, request RecallRequest, params Params) ([]Hit, error)
}

// FusionStrategy merges per-channel hit lists into one ranked candidate list.
// Channels are keyed by RecallChannel.Name so an unregistered channel still
// contributes provenance without a switch in the executor.
type FusionStrategy interface {
	Name() string
	Fuse(channels map[string][]Hit, params Params) []Candidate
}

// PlanExecutor runs the channels and fusion named by a QueryPlan. A single
// failing channel is dropped while the others may still ground the answer;
// every attempted channel failing fails the query.
type PlanExecutor struct {
	channels map[string]RecallChannel
	fusions  map[string]FusionStrategy
}

func NewPlanExecutor(channels []RecallChannel, fusions []FusionStrategy) *PlanExecutor {
	executor := &PlanExecutor{channels: map[string]RecallChannel{}, fusions: map[string]FusionStrategy{}}
	for _, channel := range channels {
		executor.RegisterChannel(channel)
	}
	for _, fusion := range fusions {
		executor.RegisterFusion(fusion)
	}
	if _, ok := executor.fusions[FusionRRF]; !ok {
		executor.RegisterFusion(RRFusion{})
	}
	return executor
}

func (e *PlanExecutor) RegisterChannel(channel RecallChannel) {
	if channel == nil {
		return
	}
	e.channels[strings.ToLower(channel.Name())] = channel
}

func (e *PlanExecutor) RegisterFusion(fusion FusionStrategy) {
	if fusion == nil {
		return
	}
	e.fusions[strings.ToLower(fusion.Name())] = fusion
}

func (e *PlanExecutor) Execute(ctx context.Context, plan QueryPlan, params Params, request RecallRequest) ([]Candidate, error) {
	selected := plan.recallChannels()
	hits := map[string][]Hit{}
	var (
		wait      sync.WaitGroup
		mu        sync.Mutex
		errs      []string
		attempted int
	)
	run := func(channel RecallChannel) {
		result, err := channel.Recall(ctx, request, params)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", channel.Name(), err))
			return
		}
		hits[channel.Name()] = result
	}
	// A request transaction pins one driver connection, and that connection
	// cannot execute two statements at once. Channels run one after another in
	// that mode; without it they stay parallel.
	serialized := tenantdb.RequestTxActive(ctx)
	for _, name := range selected {
		channel, ok := e.channels[strings.ToLower(name)]
		if !ok {
			// A plan may enable a channel this deployment has not configured;
			// the remaining channels still serve the query, as before.
			continue
		}
		attempted++
		if serialized {
			run(channel)
			continue
		}
		wait.Add(1)
		go func(channel RecallChannel) {
			defer wait.Done()
			run(channel)
		}(channel)
	}
	wait.Wait()

	if attempted == 0 {
		return nil, fmt.Errorf("no retrieval channel is enabled for this knowledge base")
	}
	if len(errs) == attempted {
		return nil, fmt.Errorf("all retrieval channels failed: %s", strings.Join(errs, "; "))
	}
	fusion, ok := e.fusions[strings.ToLower(defaultString(plan.Fusion, FusionRRF))]
	if !ok {
		return nil, fmt.Errorf("unsupported fusion strategy %q", plan.Fusion)
	}
	return fusion.Fuse(hits, params), nil
}

// RRFusion is the default reciprocal rank fusion: BM25 and cosine scores are
// not comparable, so results are merged by rank with the damping constant from
// the resolved retrieval parameters.
type RRFusion struct{}

func (RRFusion) Name() string { return FusionRRF }

func (RRFusion) Fuse(channels map[string][]Hit, params Params) []Candidate {
	names := make([]string, 0, len(channels))
	for name := range channels {
		names = append(names, name)
	}
	sort.Strings(names)
	byID := map[string]*Candidate{}
	for _, channel := range names {
		mergeHits(byID, channels[channel], channel, params.RRFK)
	}
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

func mergeHits(byID map[string]*Candidate, hits []Hit, channel string, rrfK int) {
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
		switch channel {
		case ChannelVector:
			candidate.VectorRank = rank
			candidate.VectorScore = hit.Score
		case ChannelKeyword:
			candidate.KeywordRank = rank
			candidate.KeywordScore = hit.Score
		}
		if !contains(candidate.Channels, channel) {
			candidate.Channels = append(candidate.Channels, channel)
		}
	}
}

// recallChannels lets the plan decide which channels run. Plans written before
// the channel list existed fall back to the two evidence flags.
func (p QueryPlan) recallChannels() []string {
	if len(p.Channels) > 0 {
		return p.Channels
	}
	var channels []string
	if p.KeywordEnabled {
		channels = append(channels, ChannelKeyword)
	}
	if p.VectorEnabled {
		channels = append(channels, ChannelVector)
	}
	return channels
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// KeywordChannel adapts a BM25 searcher to the recall channel contract.
type KeywordChannel struct {
	Searcher KeywordSearcher
}

func (c KeywordChannel) Name() string { return ChannelKeyword }

func (c KeywordChannel) Recall(ctx context.Context, request RecallRequest, params Params) ([]Hit, error) {
	request.TopK = params.KeywordTopK
	request.Threshold = params.KeywordThreshold
	return c.Searcher.SearchKeyword(ctx, request)
}

// maxEmbeddingSpaces bounds one query's vector fan-out. Every space costs one
// embedding call and one ANN scan; a larger mixed selection is almost always a
// mistake rather than a query that needs four-plus vector spaces.
const maxEmbeddingSpaces = 4

// VectorChannel adapts the vector index plus the per-knowledge-base embedding
// router. Targets are grouped by (model, dimension); each group is embedded and
// recalled separately, so vectors from two spaces are never compared.
type VectorChannel struct {
	Searcher VectorSearcher
	// Embedder is the deployment adapter. It serves targets when Resolver is
	// nil and is not used for a routed group.
	Embedder Embedder
	Resolver EmbeddingResolver
	// Metrics is optional; a nil recorder discards observations.
	Metrics metrics.Recorder
}

func (c VectorChannel) Name() string { return ChannelVector }

type vectorSpaceGroup struct {
	binding          EmbeddingBinding
	embedder         Embedder
	knowledgeBaseIDs []string
}

func (c VectorChannel) Recall(ctx context.Context, request RecallRequest, params Params) ([]Hit, error) {
	if params.EmbeddingTopK <= 0 || len(request.KnowledgeBaseIDs) == 0 {
		return nil, nil
	}
	groups, err := c.groups(ctx, request)
	if err != nil {
		return nil, err
	}
	perSpace := make([][]Hit, 0, len(groups))
	for _, group := range groups {
		hits, err := c.recallSpace(ctx, request, params, group)
		if err != nil {
			return nil, err
		}
		perSpace = append(perSpace, hits)
	}
	return interleaveHits(perSpace, params.EmbeddingTopK), nil
}

func (c VectorChannel) groups(ctx context.Context, request RecallRequest) ([]vectorSpaceGroup, error) {
	if c.Resolver == nil {
		if c.Embedder == nil {
			return nil, fmt.Errorf("embedding adapter is not configured")
		}
		return []vectorSpaceGroup{{
			binding:          EmbeddingBinding{Model: c.Embedder.Name(), Dimension: c.Embedder.Dimension()},
			embedder:         c.Embedder,
			knowledgeBaseIDs: request.KnowledgeBaseIDs,
		}}, nil
	}
	groups := make([]vectorSpaceGroup, 0, len(request.KnowledgeBaseIDs))
	index := make(map[string]int, len(request.KnowledgeBaseIDs))
	for _, knowledgeBaseID := range request.KnowledgeBaseIDs {
		binding, embedder, err := c.Resolver.ResolveEmbedding(ctx, request.TenantID, knowledgeBaseID)
		if err != nil {
			return nil, fmt.Errorf("resolve embedding for knowledge base %s: %w", knowledgeBaseID, err)
		}
		key := fmt.Sprintf("%s\x00%d", binding.Model, binding.Dimension)
		position, exists := index[key]
		if !exists {
			index[key] = len(groups)
			groups = append(groups, vectorSpaceGroup{binding: binding, embedder: embedder})
			position = index[key]
		}
		groups[position].knowledgeBaseIDs = append(groups[position].knowledgeBaseIDs, knowledgeBaseID)
	}
	if len(groups) > maxEmbeddingSpaces {
		return nil, fmt.Errorf("query spans %d embedding spaces, the maximum is %d; query fewer knowledge bases or align their embedding models", len(groups), maxEmbeddingSpaces)
	}
	return groups, nil
}

func (c VectorChannel) recallSpace(ctx context.Context, request RecallRequest, params Params, group vectorSpaceGroup) ([]Hit, error) {
	vectors, err := group.embedder.Embed(ctx, []string{request.Query})
	if err != nil {
		if c.Metrics != nil {
			c.Metrics.Inc("model_errors_total", metrics.Labels("model", "embedding", "stage", "query"))
		}
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embed query returned %d vectors", len(vectors))
	}
	if len(vectors[0]) != group.binding.Dimension {
		return nil, fmt.Errorf("query vector dimension %d does not match the %s binding dimension %d", len(vectors[0]), group.binding.Model, group.binding.Dimension)
	}
	spaceRequest := request
	spaceRequest.KnowledgeBaseIDs = group.knowledgeBaseIDs
	spaceRequest.EmbeddingModel = group.binding.Model
	spaceRequest.TopK = params.EmbeddingTopK
	spaceRequest.Threshold = params.VectorThreshold
	spaceRequest.EFSearch = params.VectorEFSearch
	spaceRequest.Oversample = params.VectorOversample
	spaceRequest.IterativeScan = params.VectorIterativeScan
	return c.Searcher.SearchVector(ctx, spaceRequest, vectors[0])
}

// interleaveHits merges per-space results without comparing cosine scores
// across spaces: each round takes the next-best hit from every space, which
// preserves each space's internal ordering before RRF fusion.
func interleaveHits(spaces [][]Hit, limit int) []Hit {
	if limit <= 0 {
		return nil
	}
	merged := make([]Hit, 0, limit)
	for rank := 0; len(merged) < limit; rank++ {
		progressed := false
		for _, hits := range spaces {
			if rank >= len(hits) {
				continue
			}
			progressed = true
			hit := hits[rank]
			hit.Rank = len(merged) + 1
			hit.Channel = ChannelVector
			merged = append(merged, hit)
			if len(merged) == limit {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return merged
}
