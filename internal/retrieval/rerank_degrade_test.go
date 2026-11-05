package retrieval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/circuitbreaker"
)

type recordedMetric struct {
	name   string
	labels map[string]string
}

type recordingMetrics struct {
	mu      sync.Mutex
	records []recordedMetric
}

func (r *recordingMetrics) Inc(name string, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, recordedMetric{name: name, labels: labels})
}

func (r *recordingMetrics) Observe(string, time.Duration, map[string]string) {}

func (r *recordingMetrics) has(name, key, value string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, record := range r.records {
		if record.name == name && record.labels[key] == value {
			return true
		}
	}
	return false
}

type degradingReranker struct {
	calls int
	err   error
}

func (r *degradingReranker) Name() string { return "degrading" }

func (r *degradingReranker) Rerank(_ context.Context, _ string, candidates []Candidate) ([]Candidate, error) {
	return r.record(candidates)
}

func (r *degradingReranker) RerankWithModel(_ context.Context, _, _ string, candidates []Candidate) ([]Candidate, error) {
	return r.record(candidates)
}

func (r *degradingReranker) record(candidates []Candidate) ([]Candidate, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return candidates, nil
}

type failingRerankResolver struct {
	err error
}

func (r failingRerankResolver) RerankModel(context.Context, string, string) (string, error) {
	return "", r.err
}

func degradeHits() []Hit {
	return []Hit{
		{ChunkID: "chunk-a", DocumentID: "doc-1", DocumentVersionID: "ver-1", KnowledgeBaseID: "kb-1", Ordinal: 1, Content: "alpha", Score: 0.9, Rank: 1},
		{ChunkID: "chunk-b", DocumentID: "doc-1", DocumentVersionID: "ver-1", KnowledgeBaseID: "kb-1", Ordinal: 2, Content: "beta", Score: 0.8, Rank: 2},
	}
}

func newDegradeService(reranker Reranker, resolver RerankModelResolver, recorder *recordingMetrics, breaker *circuitbreaker.Breaker) *Service {
	params := baseParams()
	params.RerankEnabled = true
	return NewService(Options{
		Reranker: reranker, RerankModels: resolver, RerankBreaker: breaker, Metrics: recorder,
		Channels: []RecallChannel{fixedHitsChannel{name: ChannelKeyword, hits: degradeHits()}},
		Defaults: params,
	})
}

func degradeScope() authz.AuthorizationScope {
	return authz.AuthorizationScope{TenantID: "tenant-1", KnowledgeBaseIDs: []string{"kb-1"}, ACLVersion: "1"}
}

func degradeInput() QueryInput {
	return QueryInput{RequestID: "req-1", Query: "question", KnowledgeBaseID: "kb-1"}
}

func assertFusedOrder(t *testing.T, set EvidenceSet) {
	t.Helper()
	if len(set.Items) != 2 || set.Items[0].ChunkID != "chunk-a" || set.Items[1].ChunkID != "chunk-b" {
		t.Fatalf("expected the pre-rerank fusion order, got %+v", set.Items)
	}
}

func TestRetrieveKeepsEvidenceWhenRerankResolutionFails(t *testing.T) {
	recorder := &recordingMetrics{}
	reranker := &degradingReranker{}
	service := newDegradeService(reranker, failingRerankResolver{err: errors.New("quota exceeded")}, recorder, nil)
	set, err := service.Retrieve(context.Background(), degradeScope(), degradeInput())
	if err != nil {
		t.Fatalf("a rerank resolution failure must not fail retrieval: %v", err)
	}
	assertFusedOrder(t, set)
	if set.RerankApplied {
		t.Fatalf("degraded retrieval must report rerankApplied=false")
	}
	if reranker.calls != 0 {
		t.Fatalf("the reranker must not run without a resolved binding, got %d calls", reranker.calls)
	}
	if !recorder.has("rerank_degraded_total", "reason", "resolve") {
		t.Fatalf("expected rerank_degraded_total{reason=resolve}, got %+v", recorder.records)
	}
	if !recorder.has("model_errors_total", "stage", "resolve") {
		t.Fatalf("expected model_errors_total{stage=resolve}, got %+v", recorder.records)
	}
}

func TestRetrieveKeepsEvidenceWhenRerankCallFails(t *testing.T) {
	recorder := &recordingMetrics{}
	reranker := &degradingReranker{err: errors.New("provider unavailable")}
	service := newDegradeService(reranker, nil, recorder, nil)
	set, err := service.Retrieve(context.Background(), degradeScope(), degradeInput())
	if err != nil {
		t.Fatalf("a rerank provider failure must not fail retrieval: %v", err)
	}
	assertFusedOrder(t, set)
	if set.RerankApplied {
		t.Fatalf("degraded retrieval must report rerankApplied=false")
	}
	if reranker.calls != 1 {
		t.Fatalf("expected one rerank attempt, got %d", reranker.calls)
	}
	if !recorder.has("rerank_degraded_total", "reason", "error") {
		t.Fatalf("expected rerank_degraded_total{reason=error}, got %+v", recorder.records)
	}
	if !recorder.has("model_errors_total", "stage", "rerank") {
		t.Fatalf("expected model_errors_total{stage=rerank}, got %+v", recorder.records)
	}
}

func TestRetrieveSkipsRerankWhileBreakerOpen(t *testing.T) {
	recorder := &recordingMetrics{}
	reranker := &degradingReranker{err: errors.New("provider unavailable")}
	breaker := circuitbreaker.New(circuitbreaker.Config{FailureThreshold: 1, Cooldown: time.Minute})
	service := newDegradeService(reranker, nil, recorder, breaker)

	if _, err := service.Retrieve(context.Background(), degradeScope(), degradeInput()); err != nil {
		t.Fatalf("first retrieval: %v", err)
	}
	set, err := service.Retrieve(context.Background(), degradeScope(), degradeInput())
	if err != nil {
		t.Fatalf("an open breaker must still serve the query: %v", err)
	}
	assertFusedOrder(t, set)
	if set.RerankApplied {
		t.Fatalf("degraded retrieval must report rerankApplied=false")
	}
	if reranker.calls != 1 {
		t.Fatalf("the open breaker must skip the provider call, got %d calls", reranker.calls)
	}
	if !recorder.has("rerank_degraded_total", "reason", "breaker_open") {
		t.Fatalf("expected rerank_degraded_total{reason=breaker_open}, got %+v", recorder.records)
	}
}
