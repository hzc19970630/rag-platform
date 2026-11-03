package retrieval

import (
	"strings"
	"testing"
)

func TestValidateIndexingStrategy(t *testing.T) {
	cases := []struct {
		name     string
		strategy IndexingStrategy
		valid    bool
		contains string
	}{
		{"evidence only", IndexingStrategy{VectorEnabled: true, KeywordEnabled: true, RequiredCapabilities: []string{"evidence"}}, true, ""},
		{"wiki enabled", IndexingStrategy{WikiEnabled: true, RequiredCapabilities: []string{"evidence", "wiki"}}, true, ""},
		{"graph enabled", IndexingStrategy{GraphEnabled: true, RequiredCapabilities: []string{"evidence"}}, true, ""},
		{"required wiki needs switch", IndexingStrategy{RequiredCapabilities: []string{"wiki"}}, false, "must enable wiki_enabled"},
		{"required graph needs switch", IndexingStrategy{RequiredCapabilities: []string{"graph"}}, false, "must enable graph_enabled"},
		{"required relation needs switch", IndexingStrategy{RequiredCapabilities: []string{"relation"}}, false, "must enable graph_enabled"},
		{"required relation with graph enabled", IndexingStrategy{GraphEnabled: true, RequiredCapabilities: []string{"evidence", "relation"}}, true, ""},
		{"unknown capability", IndexingStrategy{RequiredCapabilities: []string{"telepathy"}}, false, "unsupported"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateIndexingStrategy(testCase.strategy)
			if testCase.valid {
				if err != nil {
					t.Fatalf("expected valid strategy, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected invalid strategy")
			}
			if !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("expected error to contain %q, got %q", testCase.contains, err.Error())
			}
		})
	}
}

func TestParseIndexingStrategyDefaults(t *testing.T) {
	strategy, err := ParseIndexingStrategy("")
	if err != nil {
		t.Fatalf("parse empty strategy: %v", err)
	}
	if !strategy.VectorEnabled || !strategy.KeywordEnabled || strategy.WikiEnabled || strategy.GraphEnabled {
		t.Fatalf("unexpected default strategy: %+v", strategy)
	}
	if len(strategy.RequiredCapabilities) != 1 || strategy.RequiredCapabilities[0] != CapabilityEvidence {
		t.Fatalf("default required capabilities must be evidence-only: %+v", strategy.RequiredCapabilities)
	}
	strategy, err = ParseIndexingStrategy(`{"wiki_enabled": true}`)
	if err != nil || !strategy.WikiEnabled {
		t.Fatalf("explicit wiki switch was lost: %+v %v", strategy, err)
	}
	if len(strategy.RequiredCapabilities) != 1 || strategy.RequiredCapabilities[0] != CapabilityEvidence {
		t.Fatalf("missing required capabilities must fall back to evidence: %+v", strategy.RequiredCapabilities)
	}
}

func TestRequiresCapability(t *testing.T) {
	strategy := IndexingStrategy{RequiredCapabilities: []string{" Evidence ", "WIKI"}}
	if !requiresCapability(strategy, CapabilityEvidence) || !requiresCapability(strategy, CapabilityWiki) {
		t.Fatalf("capability matching must be case-insensitive and trimmed")
	}
	if requiresCapability(strategy, CapabilityGraph) {
		t.Fatalf("graph must not be reported as required")
	}
}
