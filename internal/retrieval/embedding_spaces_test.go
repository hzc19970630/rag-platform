package retrieval

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type stubEmbedder struct {
	name      string
	dimension int
}

func (s stubEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{make([]float32, s.dimension)}, nil
}
func (s stubEmbedder) Dimension() int { return s.dimension }
func (s stubEmbedder) Name() string   { return s.name }

type stubResolver struct {
	bindings  map[string]EmbeddingBinding
	embedders map[string]Embedder
}

func (s stubResolver) ResolveEmbedding(_ context.Context, _ string, knowledgeBaseID string) (EmbeddingBinding, Embedder, error) {
	binding, ok := s.bindings[knowledgeBaseID]
	if !ok {
		return EmbeddingBinding{}, nil, fmt.Errorf("unknown knowledge base %s", knowledgeBaseID)
	}
	return binding, s.embedders[binding.Model], nil
}

type recordingSearcher struct {
	calls []RecallRequest
	hits  map[string][]Hit
}

func (s *recordingSearcher) SearchVector(_ context.Context, request RecallRequest, embedding []float32) ([]Hit, error) {
	s.calls = append(s.calls, request)
	return s.hits[request.EmbeddingModel], nil
}

func TestVectorChannelGroupsKnowledgeBasesBySpace(t *testing.T) {
	searcher := &recordingSearcher{hits: map[string][]Hit{
		"embed-a": {{ChunkID: "a1", Score: 0.9}, {ChunkID: "a2", Score: 0.8}},
		"embed-b": {{ChunkID: "b1", Score: 0.7}, {ChunkID: "b2", Score: 0.6}},
	}}
	channel := VectorChannel{
		Searcher: searcher,
		Resolver: stubResolver{
			bindings: map[string]EmbeddingBinding{
				"kb-a": {Model: "embed-a", Dimension: 8},
				"kb-b": {Model: "embed-b", Dimension: 16},
			},
			embedders: map[string]Embedder{
				"embed-a": stubEmbedder{name: "embed-a", dimension: 8},
				"embed-b": stubEmbedder{name: "embed-b", dimension: 16},
			},
		},
	}
	hits, err := channel.Recall(context.Background(), RecallRequest{
		TenantID: "tenant-1", KnowledgeBaseIDs: []string{"kb-a", "kb-b"}, Query: "q",
	}, Params{EmbeddingTopK: 3})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(searcher.calls) != 2 {
		t.Fatalf("expected one recall per space, got %d", len(searcher.calls))
	}
	seen := map[string]bool{}
	for _, call := range searcher.calls {
		seen[call.EmbeddingModel] = true
		if len(call.KnowledgeBaseIDs) != 1 {
			t.Fatalf("model %s must only query its own knowledge base: %v", call.EmbeddingModel, call.KnowledgeBaseIDs)
		}
	}
	if !seen["embed-a"] || !seen["embed-b"] {
		t.Fatalf("both spaces must be queried: %v", seen)
	}
	if len(hits) != 3 {
		t.Fatalf("expected interleaved top-3, got %d", len(hits))
	}
	want := []string{"a1", "b1", "a2"}
	for i, chunkID := range want {
		if hits[i].ChunkID != chunkID {
			t.Fatalf("rank %d = %s, want %s", i+1, hits[i].ChunkID, chunkID)
		}
		if hits[i].Rank != i+1 || hits[i].Channel != ChannelVector {
			t.Fatalf("hit %d has rank/channel %d/%s", i, hits[i].Rank, hits[i].Channel)
		}
	}
}

func TestVectorChannelSharesOneRecallForOneSpace(t *testing.T) {
	searcher := &recordingSearcher{hits: map[string][]Hit{"embed-a": {{ChunkID: "a1", Score: 0.9}}}}
	channel := VectorChannel{
		Searcher: searcher,
		Resolver: stubResolver{
			bindings: map[string]EmbeddingBinding{
				"kb-a": {Model: "embed-a", Dimension: 8},
				"kb-b": {Model: "embed-a", Dimension: 8},
			},
			embedders: map[string]Embedder{"embed-a": stubEmbedder{name: "embed-a", dimension: 8}},
		},
	}
	if _, err := channel.Recall(context.Background(), RecallRequest{
		TenantID: "tenant-1", KnowledgeBaseIDs: []string{"kb-a", "kb-b"}, Query: "q",
	}, Params{EmbeddingTopK: 3}); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(searcher.calls) != 1 {
		t.Fatalf("one space must produce one recall, got %d", len(searcher.calls))
	}
	if len(searcher.calls[0].KnowledgeBaseIDs) != 2 {
		t.Fatalf("both knowledge bases must share the call: %v", searcher.calls[0].KnowledgeBaseIDs)
	}
}

func TestVectorChannelRejectsTooManySpaces(t *testing.T) {
	bindings := map[string]EmbeddingBinding{}
	embedders := map[string]Embedder{}
	ids := make([]string, 0, maxEmbeddingSpaces+1)
	for i := 0; i <= maxEmbeddingSpaces; i++ {
		model := fmt.Sprintf("embed-%d", i)
		kb := fmt.Sprintf("kb-%d", i)
		bindings[kb] = EmbeddingBinding{Model: model, Dimension: 8}
		embedders[model] = stubEmbedder{name: model, dimension: 8}
		ids = append(ids, kb)
	}
	channel := VectorChannel{Searcher: &recordingSearcher{}, Resolver: stubResolver{bindings: bindings, embedders: embedders}}
	_, err := channel.Recall(context.Background(), RecallRequest{TenantID: "tenant-1", KnowledgeBaseIDs: ids, Query: "q"}, Params{EmbeddingTopK: 3})
	if err == nil || !strings.Contains(err.Error(), "spans") {
		t.Fatalf("expected space limit error, got %v", err)
	}
}

func TestVectorChannelRejectsQueryDimensionMismatch(t *testing.T) {
	channel := VectorChannel{
		Searcher: &recordingSearcher{},
		Resolver: stubResolver{
			bindings:  map[string]EmbeddingBinding{"kb-a": {Model: "embed-a", Dimension: 16}},
			embedders: map[string]Embedder{"embed-a": stubEmbedder{name: "embed-a", dimension: 8}},
		},
	}
	_, err := channel.Recall(context.Background(), RecallRequest{TenantID: "tenant-1", KnowledgeBaseIDs: []string{"kb-a"}, Query: "q"}, Params{EmbeddingTopK: 3})
	if err == nil || !strings.Contains(err.Error(), "binding dimension") {
		t.Fatalf("expected dimension mismatch, got %v", err)
	}
}
