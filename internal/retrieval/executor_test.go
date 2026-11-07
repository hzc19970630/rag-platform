package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

type fakeChannel struct {
	name string
	hits []Hit
	err  error
	seen []RecallRequest
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Recall(_ context.Context, request RecallRequest, _ Params) ([]Hit, error) {
	f.seen = append(f.seen, request)
	return f.hits, f.err
}

type fixedFusion struct {
	name string
}

func (f fixedFusion) Name() string { return f.name }

func (f fixedFusion) Fuse(channels map[string][]Hit, _ Params) []Candidate {
	result := []Candidate{}
	for _, hits := range channels {
		for _, hit := range hits {
			result = append(result, Candidate{ChunkID: hit.ChunkID, Score: hit.Score})
		}
	}
	SortCandidates(result)
	return result
}

// probeChannel records how many channels execute at the same time.
type probeChannel struct {
	name    string
	current *atomic.Int32
	peak    *atomic.Int32
}

func (c probeChannel) Name() string { return c.name }

func (c probeChannel) Recall(context.Context, RecallRequest, Params) ([]Hit, error) {
	active := c.current.Add(1)
	defer c.current.Add(-1)
	for {
		observed := c.peak.Load()
		if active <= observed || c.peak.CompareAndSwap(observed, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return []Hit{{ChunkID: c.name, Score: 1}}, nil
}

func TestPlanExecutorSerializesChannelsWithRequestTransaction(t *testing.T) {
	run := func(ctx context.Context) int32 {
		var current, peak atomic.Int32
		executor := NewPlanExecutor([]RecallChannel{
			probeChannel{name: ChannelKeyword, current: &current, peak: &peak},
			probeChannel{name: ChannelVector, current: &current, peak: &peak},
		}, nil)
		plan := QueryPlan{Channels: []string{ChannelKeyword, ChannelVector}}
		if _, err := executor.Execute(ctx, plan, Params{RRFK: 60}, RecallRequest{Query: "q", TenantID: "t"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return peak.Load()
	}

	if peak := run(context.Background()); peak < 2 {
		t.Fatalf("channels did not overlap without a request transaction: peak=%d", peak)
	}
	if peak := run(tenantdb.WithTx(context.Background(), &gorm.DB{})); peak != 1 {
		t.Fatalf("channels overlapped under a request transaction: peak=%d", peak)
	}
}

func TestPlanExecutorRunsOnlyPlannedChannels(t *testing.T) {
	keyword := &fakeChannel{name: ChannelKeyword, hits: []Hit{{ChunkID: "kw", Score: 1}}}
	vector := &fakeChannel{name: ChannelVector, hits: []Hit{{ChunkID: "vec", Score: 2}}}
	executor := NewPlanExecutor([]RecallChannel{keyword, vector}, nil)
	plan := QueryPlan{Channels: []string{ChannelKeyword}, Fusion: FusionRRF}
	candidates, err := executor.Execute(context.Background(), plan, Params{RRFK: 60}, RecallRequest{Query: "q"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(keyword.seen) != 1 || len(vector.seen) != 0 {
		t.Fatalf("planned channels must be the only ones executed: keyword=%d vector=%d", len(keyword.seen), len(vector.seen))
	}
	if len(candidates) != 1 || candidates[0].ChunkID != "kw" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
}

func TestPlanExecutorToleratesOneFailingChannel(t *testing.T) {
	executor := NewPlanExecutor([]RecallChannel{
		&fakeChannel{name: ChannelKeyword, err: errors.New("index down")},
		&fakeChannel{name: ChannelVector, hits: []Hit{{ChunkID: "vec", Score: 1}}},
	}, nil)
	candidates, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelKeyword, ChannelVector}}, Params{RRFK: 60}, RecallRequest{})
	if err != nil {
		t.Fatalf("one failing channel must not fail the query: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ChunkID != "vec" {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
}

func TestPlanExecutorFailsWhenAllChannelsFail(t *testing.T) {
	executor := NewPlanExecutor([]RecallChannel{
		&fakeChannel{name: ChannelKeyword, err: errors.New("keyword down")},
		&fakeChannel{name: ChannelVector, err: errors.New("vector down")},
	}, nil)
	_, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelKeyword, ChannelVector}}, Params{RRFK: 60}, RecallRequest{})
	if err == nil {
		t.Fatal("expected all-channel failure")
	}
	for _, want := range []string{"keyword", "vector"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must name the failing channel %q", err, want)
		}
	}
}

func TestPlanExecutorSkipsUnconfiguredChannel(t *testing.T) {
	executor := NewPlanExecutor([]RecallChannel{&fakeChannel{name: ChannelKeyword, hits: []Hit{{ChunkID: "kw"}}}}, nil)
	candidates, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelKeyword, "future"}}, Params{RRFK: 60}, RecallRequest{})
	if err != nil {
		t.Fatalf("unconfigured channel must be skipped: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
	_, err = executor.Execute(context.Background(), QueryPlan{Channels: []string{"future"}}, Params{RRFK: 60}, RecallRequest{})
	if err == nil {
		t.Fatal("a plan with no configured channel must fail")
	}
}

func TestPlanExecutorRegistersChannelWithoutChangingFusion(t *testing.T) {
	extra := &fakeChannel{name: "glossary", hits: []Hit{{ChunkID: "glossary-1", Score: 1}}}
	keyword := &fakeChannel{name: ChannelKeyword, hits: []Hit{{ChunkID: "kw", Score: 1}}}
	executor := NewPlanExecutor([]RecallChannel{keyword}, nil)
	executor.RegisterChannel(extra)
	fused, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelKeyword, "glossary"}, Fusion: FusionRRF}, Params{RRFK: 60}, RecallRequest{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(fused) != 2 {
		t.Fatalf("a registered channel must contribute without a code change: %+v", fused)
	}
	found := false
	for _, candidate := range fused {
		if candidate.ChunkID == "glossary-1" && len(candidate.Channels) == 1 && candidate.Channels[0] == "glossary" {
			found = true
		}
	}
	if !found {
		t.Fatalf("registered channel provenance was lost: %+v", fused)
	}
}

func TestPlanExecutorHonoursNamedFusion(t *testing.T) {
	executor := NewPlanExecutor([]RecallChannel{
		&fakeChannel{name: ChannelKeyword, hits: []Hit{{ChunkID: "a", Score: 1}}},
	}, []FusionStrategy{fixedFusion{name: "first"}})
	candidates, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelKeyword}, Fusion: "first"}, Params{}, RecallRequest{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ChunkID != "a" {
		t.Fatalf("named fusion did not run: %+v", candidates)
	}
	if _, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelKeyword}, Fusion: "missing"}, Params{}, RecallRequest{}); err == nil {
		t.Fatal("unknown fusion strategy must fail explicitly")
	}
}

func TestDefaultRRFMatchesLegacyFusion(t *testing.T) {
	vectorHits := []Hit{hit("v1", 9), hit("shared", 8)}
	keywordHits := []Hit{hit("k1", 8), hit("shared", 7)}
	legacy := fuseFRL(vectorHits, keywordHits, 60)
	executor := NewPlanExecutor([]RecallChannel{
		&fakeChannel{name: ChannelVector, hits: vectorHits},
		&fakeChannel{name: ChannelKeyword, hits: keywordHits},
	}, nil)
	planned, err := executor.Execute(context.Background(), QueryPlan{Channels: []string{ChannelVector, ChannelKeyword}, Fusion: FusionRRF}, Params{RRFK: 60}, RecallRequest{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(planned) != len(legacy) {
		t.Fatalf("fused count %d != baseline %d", len(planned), len(legacy))
	}
	for index := range planned {
		if planned[index].ChunkID != legacy[index].ChunkID || planned[index].Score != legacy[index].Score {
			t.Fatalf("candidate %d differs from baseline: %+v vs %+v", index, planned[index], legacy[index])
		}
	}
}

func TestEvidencePlannerRecordsChannelsAndFusion(t *testing.T) {
	strategy := IndexingStrategy{VectorEnabled: true, KeywordEnabled: true, RequiredCapabilities: []string{CapabilityEvidence}}
	plan, err := EvidencePlanner{}.Plan(context.Background(), PlanInput{Strategy: strategy, Params: Params{TopK: 8}, Query: QueryInput{RequestID: "req"}})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Channels) != 2 || plan.Fusion != FusionRRF {
		t.Fatalf("plan must record its executable channels: %+v", plan)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	for _, field := range []string{`"channels"`, `"fusion"`, `"vectorEnabled"`, `"keywordEnabled"`, `"topK"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("QueryPlan JSON lost %s: %s", field, encoded)
		}
	}
}

func TestCapabilityRegistryIsEnumerableAndCaseInsensitive(t *testing.T) {
	names := CapabilityNames()
	if len(names) != 4 {
		t.Fatalf("unexpected capability catalog: %v", names)
	}
	spec, ok := LookupCapability(" WIKI ")
	if !ok || spec.Name != CapabilityWiki || spec.Projection != projectionWiki {
		t.Fatalf("lookup did not normalize and resolve wiki: %+v %v", spec, ok)
	}
	if _, ok := LookupCapability("telepathy"); ok {
		t.Fatal("unknown capability must not resolve")
	}
	gates, err := requiredProjectionGates(IndexingStrategy{RequiredCapabilities: []string{"GRAPH", "evidence"}})
	if err != nil || !gates[projectionRelation] || len(gates) != 1 {
		t.Fatalf("graph must gate on the relation projection: %+v %v", gates, err)
	}
	if _, err := requiredProjectionGates(IndexingStrategy{RequiredCapabilities: []string{"telepathy"}}); err == nil {
		t.Fatal("unknown capability must fail the gate resolution")
	}
}
