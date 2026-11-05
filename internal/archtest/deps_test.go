package archtest

import "testing"

const module = "example.com/mod"

func TestCheckRejectsIllegalEdges(t *testing.T) {
	cases := []struct {
		name    string
		pkg     string
		imports []string
		rule    string
	}{
		{
			name:    "domain imports global config",
			pkg:     module + "/internal/conversation",
			imports: []string{module + "/internal/config"},
			rule:    RuleDomainConfig,
		},
		{
			name:    "domain imports composition root",
			pkg:     module + "/internal/retrieval",
			imports: []string{module + "/internal/app"},
			rule:    RuleDomainComposition,
		},
		{
			name:    "domain imports provider adapter",
			pkg:     module + "/internal/ingestion",
			imports: []string{module + "/internal/platform/queue"},
			rule:    RuleDomainAdapter,
		},
		{
			name:    "transport imports application",
			pkg:     module + "/internal/httpapi",
			imports: []string{module + "/internal/app"},
			rule:    RuleTransportApp,
		},
		{
			name:    "platform adapter imports transport",
			pkg:     module + "/internal/platform/search",
			imports: []string{module + "/internal/httpapi"},
			rule:    RuleAdapterComposition,
		},
		{
			name:    "platform adapter imports domain implementation",
			pkg:     module + "/internal/platform/storage",
			imports: []string{module + "/internal/knowledgeprojection"},
			rule:    RuleAdapterDomain,
		},
		{
			name:    "platform adapter imports model registry",
			pkg:     module + "/internal/platform/queue",
			imports: []string{module + "/internal/modelregistry"},
			rule:    RuleAdapterDomain,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			violations := Check(module, []Package{{ImportPath: testCase.pkg, Imports: testCase.imports}})
			if len(violations) != 1 {
				t.Fatalf("expected one violation, got %#v", violations)
			}
			if violations[0].Rule != testCase.rule {
				t.Fatalf("rule = %q, want %q", violations[0].Rule, testCase.rule)
			}
		})
	}
}

func TestCheckAllowsPortAndSharedEdges(t *testing.T) {
	packages := []Package{
		{ImportPath: module + "/internal/knowledgebase", Imports: []string{module + "/internal/config"}},
		{ImportPath: module + "/internal/retrieval", Imports: []string{module + "/internal/contracts/metrics", module + "/internal/platform/persistence/models"}},
		{ImportPath: module + "/internal/platform/llm", Imports: []string{module + "/internal/contracts/model"}},
		{ImportPath: module + "/internal/platform/evidenceindex", Imports: []string{module + "/internal/ingestion"}},
		{ImportPath: module + "/internal/platform/rerank", Imports: []string{module + "/internal/retrieval"}},
		{ImportPath: module + "/internal/platform/relationstore", Imports: []string{module + "/internal/relationstore"}},
		{ImportPath: module + "/internal/platform/evidencestore", Imports: []string{module + "/internal/status", module + "/internal/apperror", module + "/internal/contracts/evidence"}},
	}
	if violations := Check(module, packages); len(violations) != 0 {
		t.Fatalf("unexpected violations: %#v", violations)
	}
}
