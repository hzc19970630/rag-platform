package retrieval

import (
	"context"
	"testing"

	"github.com/example/rag-platform/internal/config"
)

func baseParams() Params {
	return config.RetrievalConfig{
		TopK: 8, EmbeddingTopK: 20, KeywordTopK: 20,
		VectorThreshold: 0.2, KeywordThreshold: 0, RerankThreshold: -10,
		ContextTokenLimit: 3000, AdjacentExpansion: 1, MaxAdjacentChunks: 2,
		RRFK: 60, ExpansionScoreFactor: 0.5,
	}
}

func TestResolveParamsAppliesKnowledgeBaseThenSession(t *testing.T) {
	params, err := ResolveParams(baseParams(), `{"top_k": 5, "vector_threshold": 0.5}`, `{"top_k": 3}`)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if params.TopK != 3 {
		t.Fatalf("session tier must win over the knowledge base tier, got top_k=%d", params.TopK)
	}
	if params.VectorThreshold != 0.5 {
		t.Fatalf("knowledge base tier must be preserved, got vector_threshold=%v", params.VectorThreshold)
	}
	if params.KeywordTopK != 20 {
		t.Fatalf("untouched values must keep the global default, got keyword_top_k=%d", params.KeywordTopK)
	}
}

func TestResolveParamsRejectsOutOfRangeSessionOverride(t *testing.T) {
	cases := []string{
		`{"top_k": 201}`,
		`{"vector_threshold": 1.5}`,
		`{"vector_threshold": -0.1}`,
		`{"rerank_threshold": 11}`,
		`{"context_token_limit": 0}`,
		`{"rrf_k": 0}`,
		`{"rrf_k": 201}`,
		`{"expansion_score_factor": 1.5}`,
		`{"expansion_score_factor": -0.1}`,
	}
	for _, override := range cases {
		if _, err := ResolveParams(baseParams(), "", override); err == nil {
			t.Fatalf("expected %s to be rejected", override)
		}
	}
}

func TestResolveParamsAppliesFusionAndExpansionOverrides(t *testing.T) {
	params, err := ResolveParams(baseParams(), `{"rrf_k": 10}`, `{"expansion_score_factor": 0.25}`)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if params.RRFK != 10 {
		t.Fatalf("the knowledge base tier must set rrf_k, got %d", params.RRFK)
	}
	if params.ExpansionScoreFactor != 0.25 {
		t.Fatalf("the session tier must set expansion_score_factor, got %v", params.ExpansionScoreFactor)
	}
}

func TestResolveParamsRejectsMalformedOverride(t *testing.T) {
	if _, err := ResolveParams(baseParams(), "", `{"top_k": "many"}`); err == nil {
		t.Fatalf("expected a malformed override to be rejected")
	}
	if _, err := ResolveParams(baseParams(), `not json`, ""); err == nil {
		t.Fatalf("expected a malformed knowledge base config to be rejected")
	}
}

func TestValidateOverrideAllowsPartialUpdates(t *testing.T) {
	topK := 12
	if err := ValidateOverride(ParamsOverride{TopK: &topK}); err != nil {
		t.Fatalf("a partial override must validate: %v", err)
	}
	tooLarge := 500
	if err := ValidateOverride(ParamsOverride{TopK: &tooLarge}); err == nil {
		t.Fatalf("expected an out-of-range top_k to be rejected")
	}
}

func TestParseIndexingStrategyDefaultsToEvidenceHybrid(t *testing.T) {
	strategy, err := ParseIndexingStrategy("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strategy.VectorEnabled || !strategy.KeywordEnabled || strategy.WikiEnabled || strategy.GraphEnabled {
		t.Fatalf("unexpected default strategy: %+v", strategy)
	}
	strategy, err = ParseIndexingStrategy(`{"vector_enabled": false, "keyword_enabled": true}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strategy.VectorEnabled || !strategy.KeywordEnabled {
		t.Fatalf("stored switches must be honoured: %+v", strategy)
	}
	if len(strategy.RequiredCapabilities) != 1 || strategy.RequiredCapabilities[0] != "evidence" {
		t.Fatalf("expected the evidence capability to remain required: %+v", strategy.RequiredCapabilities)
	}
}

func hit(id string, rank int) Hit {
	return Hit{ChunkID: id, DocumentID: "doc-1", DocumentVersionID: "ver-1", KnowledgeBaseID: "kb-1", Ordinal: rank, Content: id, Rank: rank}
}

func TestFuseFRLRanksAgreementHighest(t *testing.T) {
	vectorHits := []Hit{hit("a", 1), hit("b", 2)}
	keywordHits := []Hit{hit("b", 1), hit("c", 2)}
	fused := fuseFRL(vectorHits, keywordHits, 60)

	if len(fused) != 3 {
		t.Fatalf("expected 3 fused candidates, got %d", len(fused))
	}
	// b appears in both channels, so it must outrank the single-channel hits.
	if fused[0].ChunkID != "b" {
		t.Fatalf("expected the agreement candidate first, got %s", fused[0].ChunkID)
	}
	if len(fused[0].Channels) != 1 || fused[0].Channels[0] != ChannelBoth {
		t.Fatalf("expected the agreement candidate to be labelled, got %v", fused[0].Channels)
	}
}

func TestFuseFRLUsesConfiguredConstant(t *testing.T) {
	vectorHits := []Hit{hit("a", 1)}
	keywordHits := []Hit{hit("a", 1)}
	// A chunk ranked first by both channels fuses to 2/(k+1): 0.0328 at the
	// default k=60, 0.18 at k=10. This is the band the API reports as `score`.
	if got := fuseFRL(vectorHits, keywordHits, 60)[0].Score; got < 0.0327 || got > 0.0329 {
		t.Fatalf("expected 2/61 with the default constant, got %v", got)
	}
	if got := fuseFRL(vectorHits, keywordHits, 10)[0].Score; got < 0.181 || got > 0.183 {
		t.Fatalf("expected 2/11 with rrf_k=10, got %v", got)
	}
}

func TestFuseFRLIsDeterministicForTies(t *testing.T) {
	first := fuseFRL([]Hit{hit("x", 1)}, []Hit{hit("y", 1)}, 60)
	second := fuseFRL([]Hit{hit("x", 1)}, []Hit{hit("y", 1)}, 60)
	if first[0].ChunkID != second[0].ChunkID {
		t.Fatalf("fusion order must be stable: %s vs %s", first[0].ChunkID, second[0].ChunkID)
	}
}

func TestDedupeKeepsFirstOccurrence(t *testing.T) {
	candidates := []Candidate{{ChunkID: "a", Score: 1}, {ChunkID: "a", Score: 0.5}, {ChunkID: "b"}}
	result := dedupe(candidates)
	if len(result) != 2 || result[0].Score != 1 {
		t.Fatalf("unexpected dedupe result: %+v", result)
	}
}

func TestApplyTokenLimitTrimsTailAndKeepsRanking(t *testing.T) {
	candidates := []Candidate{{ChunkID: "a", Content: "aaaa"}, {ChunkID: "b", Content: "bbbb"}, {ChunkID: "c", Content: "cccc"}}
	kept, tokens := applyTokenLimit(candidates, 8)
	if len(kept) != 2 {
		t.Fatalf("expected the tail to be trimmed, got %d candidates", len(kept))
	}
	if kept[0].ChunkID != "a" || kept[1].ChunkID != "b" {
		t.Fatalf("trimming must keep the highest ranked evidence: %+v", kept)
	}
	// Each short chunk costs 4 estimated tokens: 2 for the content plus 1 for the
	// empty heading, each rounded up.
	if tokens != 8 {
		t.Fatalf("unexpected token estimate %d", tokens)
	}
}

func TestApplyTokenLimitAlwaysKeepsOneCandidate(t *testing.T) {
	candidates := []Candidate{{ChunkID: "a", Content: "a very long chunk that exceeds the budget"}}
	kept, _ := applyTokenLimit(candidates, 1)
	if len(kept) != 1 {
		t.Fatalf("a single candidate must not be dropped entirely")
	}
}

func TestLexicalRerankerReordersByOverlap(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "a", Content: "unrelated text", Score: 0.9},
		{ChunkID: "b", Content: "retrieval threshold behaviour", Score: 0.1},
	}
	reranked, err := LexicalReranker{}.Rerank(context.Background(), "retrieval threshold", candidates)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if reranked[0].ChunkID != "b" {
		t.Fatalf("expected the reranker to promote the overlapping chunk, got %s", reranked[0].ChunkID)
	}
	if reranked[0].RerankScore == nil || *reranked[0].RerankScore != 1.0 {
		t.Fatalf("expected a normalized rerank score of 1.0 for a full match, got %v", reranked[0].RerankScore)
	}
	// The fused score is what callers report as the evidence score, so rerank
	// must not overwrite it.
	if reranked[0].Score != 0.1 || reranked[1].Score != 0.9 {
		t.Fatalf("rerank must keep the fused score: %+v", reranked)
	}
}

func TestRerankThresholdAndTopKNarrowTheRerankedSet(t *testing.T) {
	one, zero := 1.0, 0.0
	candidates := []Candidate{
		{ChunkID: "a", RerankScore: &one, Score: 0.03},
		{ChunkID: "b", RerankScore: &zero, Score: 0.02},
		{ChunkID: "c", Score: 0.01},
	}
	thresholded := aboveRerankThreshold(candidates, 0.5)
	// Only the reranker's own verdict excludes a candidate: a candidate it never
	// scored stays.
	if len(thresholded) != 2 || thresholded[0].ChunkID != "a" || thresholded[1].ChunkID != "c" {
		t.Fatalf("unexpected threshold result: %+v", thresholded)
	}
	if kept := truncateReranked(candidates, 2); len(kept) != 2 || kept[0].ChunkID != "a" {
		t.Fatalf("expected the top 2 reranked candidates, got %+v", kept)
	}
	if kept := truncateReranked(candidates, 0); len(kept) != 3 {
		t.Fatalf("rerank_top_k=0 must not truncate, got %d candidates", len(kept))
	}
}

func TestSortCandidatesKeepsRerankOrderOverFusedScore(t *testing.T) {
	high, low := 0.9, 0.1
	// Fused order would put "a" first; the reranker prefers "b".
	candidates := []Candidate{
		{ChunkID: "a", Score: 0.0328, RerankScore: &low},
		{ChunkID: "b", Score: 0.0164, RerankScore: &high},
		{ChunkID: "c", Score: 0.0160},
	}
	SortCandidates(candidates)
	if candidates[0].ChunkID != "b" {
		t.Fatalf("expected the rerank order to survive the sort, got %s", candidates[0].ChunkID)
	}
	// The two scales are not comparable, and that is deliberate: a reranker
	// scores everything it is handed, so a candidate it never scored (a partial
	// provider response) sorts below every endorsed one instead of being dropped.
	if candidates[1].ChunkID != "a" || candidates[2].ChunkID != "c" {
		t.Fatalf("unexpected order for scored and unscored candidates: %+v", candidates)
	}
}

func TestNoopRerankerIsNotCountedAsApplied(t *testing.T) {
	if (NoopReranker{}).Name() != "none" {
		t.Fatalf("the no-op reranker must be identifiable so it is not reported as applied")
	}
}
