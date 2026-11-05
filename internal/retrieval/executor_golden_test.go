package retrieval

import (
	"context"
	"math"
	"testing"
)

type fixedHitsChannel struct {
	name string
	hits []Hit
}

func (c fixedHitsChannel) Name() string { return c.name }

func (c fixedHitsChannel) Recall(_ context.Context, _ RecallRequest, _ Params) ([]Hit, error) {
	return c.hits, nil
}

// C1 fixed-query golden: with keyword and vector hits held constant, RRF must
// keep the same order, scores and provenance. Any intentional change to fusion
// or ranking has to update this baseline, which is what makes a retrieval
// regression visible instead of silently changing answers.
func TestFixedQueryFusionGolden(t *testing.T) {
	executor := NewPlanExecutor([]RecallChannel{
		fixedHitsChannel{name: ChannelKeyword, hits: []Hit{
			{ChunkID: "chunk-a", DocumentID: "doc-a", KnowledgeBaseID: "kb-a", Content: "alpha", Score: 0.9, Rank: 1},
			{ChunkID: "chunk-b", DocumentID: "doc-b", KnowledgeBaseID: "kb-a", Content: "beta", Score: 0.8, Rank: 2},
		}},
		fixedHitsChannel{name: ChannelVector, hits: []Hit{
			{ChunkID: "chunk-b", DocumentID: "doc-b", KnowledgeBaseID: "kb-a", Content: "beta", Score: 0.95, Rank: 1},
			{ChunkID: "chunk-c", DocumentID: "doc-c", KnowledgeBaseID: "kb-a", Content: "gamma", Score: 0.85, Rank: 2},
		}},
	}, nil)

	candidates, err := executor.Execute(context.Background(),
		QueryPlan{Channels: []string{ChannelKeyword, ChannelVector}, Fusion: FusionRRF},
		Params{RRFK: 60}, RecallRequest{Query: "fixed query"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	type want struct {
		chunkID     string
		score       float64
		keywordRank int
		vectorRank  int
		channels    string
	}
	baseline := []want{
		{chunkID: "chunk-b", score: 1.0/62 + 1.0/61, keywordRank: 2, vectorRank: 1, channels: ChannelBoth},
		{chunkID: "chunk-a", score: 1.0 / 61, keywordRank: 1, channels: ChannelKeyword},
		{chunkID: "chunk-c", score: 1.0 / 62, vectorRank: 2, channels: ChannelVector},
	}
	if len(candidates) != len(baseline) {
		t.Fatalf("fusion returned %d candidates, want %d: %+v", len(candidates), len(baseline), candidates)
	}
	for index, expected := range baseline {
		got := candidates[index]
		if got.ChunkID != expected.chunkID {
			t.Fatalf("candidate %d = %s, want %s", index, got.ChunkID, expected.chunkID)
		}
		if math.Abs(got.Score-expected.score) > 1e-12 {
			t.Fatalf("%s score = %.15f, want %.15f", got.ChunkID, got.Score, expected.score)
		}
		if got.KeywordRank != expected.keywordRank || got.VectorRank != expected.vectorRank {
			t.Fatalf("%s provenance = keyword:%d vector:%d, want keyword:%d vector:%d",
				got.ChunkID, got.KeywordRank, got.VectorRank, expected.keywordRank, expected.vectorRank)
		}
		if len(got.Channels) != 1 || got.Channels[0] != expected.channels {
			t.Fatalf("%s channels = %v, want [%s]", got.ChunkID, got.Channels, expected.channels)
		}
	}
	if candidates[0].VectorScore != 0.95 || candidates[0].KeywordScore != 0.8 {
		t.Fatalf("channel scores must be preserved: %+v", candidates[0])
	}
}
