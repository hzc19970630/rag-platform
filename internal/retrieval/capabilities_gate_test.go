package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
)

type fakeJoin struct {
	status CapabilityStatus
	err    error
}

func (f fakeJoin) CapabilityStatus(context.Context, string, string) (CapabilityStatus, error) {
	return f.status, f.err
}

func TestEnforceCapabilitiesGatesEachProjectionIndependently(t *testing.T) {
	scope := authz.AuthorizationScope{TenantID: "tenant-1"}
	evidenceOnly := IndexingStrategy{VectorEnabled: true, KeywordEnabled: true, RequiredCapabilities: []string{"evidence"}}
	wikiRequired := IndexingStrategy{WikiEnabled: true, GraphEnabled: true, RequiredCapabilities: []string{"evidence", "wiki"}}
	relationRequired := IndexingStrategy{WikiEnabled: true, GraphEnabled: true, RequiredCapabilities: []string{"evidence", "relation"}}
	graphRequired := IndexingStrategy{WikiEnabled: true, GraphEnabled: true, RequiredCapabilities: []string{"evidence", "graph"}}
	partial := CapabilityStatus{WikiReady: true, RelationReady: false}

	cases := []struct {
		name     string
		strategy IndexingStrategy
		status   CapabilityStatus
		join     bool
		ready    bool
		contains string
	}{
		{"evidence only never consults the join", evidenceOnly, CapabilityStatus{}, false, true, ""},
		{"wiki ready survives a failed relation run", wikiRequired, partial, true, true, ""},
		{"wiki not ready blocks a wiki query", wikiRequired, CapabilityStatus{RelationReady: true}, true, false, "wiki projection is not ready"},
		{"relation ready satisfies graph capability", graphRequired, CapabilityStatus{RelationReady: true}, true, true, ""},
		{"relation required blocks on a failed run", relationRequired, partial, true, false, "relation projection is not ready"},
		{"missing join blocks a projection query", wikiRequired, CapabilityStatus{}, false, false, "join is not configured"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options := Options{}
			if testCase.join {
				options.Join = fakeJoin{status: testCase.status}
			}
			service := NewService(options)
			err := service.enforceCapabilities(context.Background(), scope, []string{"kb-1"}, testCase.strategy, "kb-1")
			if testCase.ready {
				if err != nil {
					t.Fatalf("expected ready, got %v", err)
				}
				return
			}
			if !errors.Is(err, apperror.ErrNotReady) {
				t.Fatalf("expected ErrNotReady, got %v", err)
			}
			if testCase.contains != "" && !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("expected error to contain %q, got %q", testCase.contains, err.Error())
			}
		})
	}
}
