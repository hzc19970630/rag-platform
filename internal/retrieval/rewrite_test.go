package retrieval

import (
	"context"
	"errors"
	"testing"

	"github.com/example/rag-platform/internal/authz"
)

type fakeKeywordSearcher struct {
	query string
	hits  []Hit
}

func (f *fakeKeywordSearcher) SearchKeyword(_ context.Context, request RecallRequest) ([]Hit, error) {
	f.query = request.Query
	return f.hits, nil
}

type fakeRewriter struct {
	output string
	err    error
	called bool
}

func (f *fakeRewriter) Rewrite(context.Context, string, []HistoryTurn) (string, error) {
	f.called = true
	return f.output, f.err
}

func retrievalScope() authz.AuthorizationScope {
	return authz.AuthorizationScope{TenantID: "tenant-a", KnowledgeBaseIDs: []string{"kb-a"}, ACLVersion: "v1"}
}

func retrievalHit() Hit {
	return Hit{ChunkID: "chunk-a", DocumentID: "doc-a", KnowledgeBaseID: "kb-a", Content: "evidence text", Rank: 1}
}

func testParams() Params {
	return Params{TopK: 10, KeywordTopK: 10, ContextTokenLimit: 3000, RRFK: 60, ExpansionScoreFactor: 0.5}
}

func TestRetrieveUsesRewrittenQueryForRecall(t *testing.T) {
	keyword := &fakeKeywordSearcher{hits: []Hit{retrievalHit()}}
	rewriter := &fakeRewriter{output: "standalone question"}
	service := NewService(Options{
		Keyword: keyword, Rewriter: rewriter,
		Defaults: testParams(),
	})
	set, err := service.Retrieve(context.Background(), retrievalScope(), QueryInput{
		Query:   "what about the second one",
		History: []HistoryTurn{{Role: "user", Content: "first question"}, {Role: "assistant", Content: "first answer"}},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !rewriter.called {
		t.Fatalf("rewriter was not called with history")
	}
	if keyword.query != "standalone question" {
		t.Fatalf("recall query = %q, want rewritten query", keyword.query)
	}
	if set.Query != "what about the second one" {
		t.Fatalf("evidence set must keep the original question, got %q", set.Query)
	}
	if set.Plan.Rewrite != "standalone question" {
		t.Fatalf("plan rewrite = %q", set.Plan.Rewrite)
	}
	if len(set.Items) != 1 {
		t.Fatalf("items = %d", len(set.Items))
	}
}

func TestRetrieveFallsBackWhenRewriteFails(t *testing.T) {
	keyword := &fakeKeywordSearcher{hits: []Hit{retrievalHit()}}
	service := NewService(Options{
		Keyword: keyword, Rewriter: &fakeRewriter{err: errors.New("model down")},
		Defaults: testParams(),
	})
	if _, err := service.Retrieve(context.Background(), retrievalScope(), QueryInput{
		Query:   "original question",
		History: []HistoryTurn{{Role: "user", Content: "prior"}},
	}); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if keyword.query != "original question" {
		t.Fatalf("recall query = %q, want original after rewrite failure", keyword.query)
	}
}

func TestRetrieveSkipsRewriteWithoutHistory(t *testing.T) {
	rewriter := &fakeRewriter{output: "should not be used"}
	keyword := &fakeKeywordSearcher{hits: []Hit{retrievalHit()}}
	service := NewService(Options{
		Keyword: keyword, Rewriter: rewriter,
		Defaults: testParams(),
	})
	if _, err := service.Retrieve(context.Background(), retrievalScope(), QueryInput{Query: "single turn"}); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if rewriter.called {
		t.Fatalf("rewriter must not run without history")
	}
	if keyword.query != "single turn" {
		t.Fatalf("recall query = %q", keyword.query)
	}
}
