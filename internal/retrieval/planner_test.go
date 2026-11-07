package retrieval

import (
	"context"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/authz"
)

func TestEvidencePlannerMatchesLegacyPlan(t *testing.T) {
	params := Params{TopK: 8, RerankEnabled: true}
	strategy := IndexingStrategy{VectorEnabled: true, KeywordEnabled: true, RequiredCapabilities: []string{CapabilityEvidence}}
	scope := authz.AuthorizationScope{TenantID: "tenant-a", ACLVersion: "tenant:tenant-a:3"}
	input := PlanInput{
		Scope:                  scope,
		Query:                  QueryInput{RequestID: "req-a", Query: "how does it work"},
		TargetKnowledgeBaseIDs: []string{"kb-a"},
		Strategy:               strategy,
		Params:                 params,
		History:                []HistoryTurn{{Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"}},
	}
	plan, err := (EvidencePlanner{}).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.SchemaVersion != SchemaQueryPlan || plan.Intent != IntentFact || plan.Mode != ModeEvidence {
		t.Fatalf("plan identity = %+v", plan)
	}
	if plan.RequestID != "req-a" || plan.TenantID != "tenant-a" || plan.AuthorizationScopeVersion != "tenant:tenant-a:3" {
		t.Fatalf("plan scope = %+v", plan)
	}
	if plan.Rewrite != "how does it work" || plan.TopK != 8 || !plan.VectorEnabled || !plan.KeywordEnabled || !plan.RerankEnabled {
		t.Fatalf("plan switches = %+v", plan)
	}
	if len(plan.KnowledgeBaseIDs) != 1 || plan.KnowledgeBaseIDs[0] != "kb-a" {
		t.Fatalf("plan knowledge bases = %+v", plan.KnowledgeBaseIDs)
	}
	if plan.HistoryTurns != 2 {
		t.Fatalf("history turns = %d, want 2", plan.HistoryTurns)
	}
}

func TestServiceUsesInjectedPlanner(t *testing.T) {
	service := NewService(Options{Planner: stubPlanner{plan: QueryPlan{Intent: "custom"}}})
	if service.planner == nil {
		t.Fatalf("default planner must be installed")
	}
	plan, err := service.planner.Plan(context.Background(), PlanInput{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Intent != "custom" {
		t.Fatalf("injected planner was not used: %+v", plan)
	}
}

type stubPlanner struct{ plan QueryPlan }

func (s stubPlanner) Plan(context.Context, PlanInput) (QueryPlan, error) { return s.plan, nil }
