package retrieval

import (
	"context"
	"fmt"
	"github.com/example/rag-platform/internal/contracts/metrics"
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

// Channels is the configured channel set, sorted for stable diagnostics.
func (e *PlanExecutor) Channels() []string {
	names := make([]string, 0, len(e.channels))
	for name := range e.channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	for _, name := range selected {
		channel, ok := e.channels[strings.ToLower(name)]
		if !ok {
			// A plan may enable a channel this deployment has not configured;
			// the remaining channels still serve the query, as before.
			continue
		}
		attempted++
		wait.Add(1)
		go func(channel RecallChannel) {
			defer wait.Done()
			result, err := channel.Recall(ctx, request, params)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", channel.Name(), err))
				return
			}
			hits[channel.Name()] = result
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

// VectorChannel adapts the vector index plus its query embedder. It refuses to
// embed for a knowledge base bound to another model or dimension, so two vector
// spaces are never compared.
type VectorChannel struct {
	Searcher VectorSearcher
	Embedder Embedder
	Verify   func(ctx context.Context, tenantID string, knowledgeBaseIDs []string) error
	// Metrics is optional; a nil recorder discards observations.
	Metrics metrics.Recorder
}

func (c VectorChannel) Name() string { return ChannelVector }

func (c VectorChannel) Recall(ctx context.Context, request RecallRequest, params Params) ([]Hit, error) {
	if c.Verify != nil {
		if err := c.Verify(ctx, request.TenantID, request.KnowledgeBaseIDs); err != nil {
			return nil, fmt.Errorf("embedding binding mismatch: %w", err)
		}
	}
	vectors, err := c.Embedder.Embed(ctx, []string{request.Query})
	if err != nil {
		if c.Metrics != nil {
			c.Metrics.Inc("model_errors_total", metrics.Labels("model", "embedding", "stage", "query"))
		}
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embed query returned %d vectors", len(vectors))
	}
	if len(vectors[0]) != c.Embedder.Dimension() {
		return nil, fmt.Errorf("query vector dimension %d does not match configured dimension %d", len(vectors[0]), c.Embedder.Dimension())
	}
	request.EmbeddingModel = c.Embedder.Name()
	request.TopK = params.EmbeddingTopK
	request.Threshold = params.VectorThreshold
	return c.Searcher.SearchVector(ctx, request, vectors[0])
}
