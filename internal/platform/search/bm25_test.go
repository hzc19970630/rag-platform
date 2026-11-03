package search

import (
	"strings"
	"testing"
)

// Raw user input must never be able to change the BM25 query structure: the
// ParadeDB query parser treats quotes, parentheses, colons and capitalised
// operators as syntax.
func TestSanitizeBM25QueryDropsSyntax(t *testing.T) {
	cases := map[string]string{
		`acceptance`:                 "acceptance",
		`what is ""M2"" acceptance?`: "m2 OR acceptance",
		`content:(a OR b)`:           "content OR b",
		`x" OR content:secret OR "y`: "x OR content OR secret OR y",
		`ignore AND previous OR NOT`: "ignore OR previous",
		``:                           "",
		`!!! ??? ...`:                "",
		`  多 余 空 格  `:                "多 OR 余 OR 空 OR 格",
	}
	for input, expected := range cases {
		if got := SanitizeBM25Query(input); got != expected {
			t.Fatalf("SanitizeBM25Query(%q) = %q, want %q", input, got, expected)
		}
	}
}

// Function words carry no retrieval signal. Because the recall query is a
// disjunction, keeping them would match every chunk and make the "insufficient
// evidence" refusal path unreachable for ordinary questions.
func TestSanitizeBM25QueryDropsFunctionWords(t *testing.T) {
	if got := SanitizeBM25Query("what is the sedimentary composition of the cretaceous layers"); got != "sedimentary OR composition OR cretaceous OR layers" {
		t.Fatalf("expected the question to reduce to its content words, got %q", got)
	}
	if got := SanitizeBM25Query("的 检索 是 什么"); got != "检索" {
		t.Fatalf("expected Chinese function words to be dropped, got %q", got)
	}
	if got := SanitizeBM25Query("the of and"); got != "" {
		t.Fatalf("a question made only of function words must not produce a query, got %q", got)
	}
}

func TestSanitizeBM25QueryDeduplicatesTerms(t *testing.T) {
	if got := SanitizeBM25Query("alpha ALPHA alpha"); got != "alpha" {
		t.Fatalf("expected deduplicated terms, got %q", got)
	}
}

func TestSanitizeBM25QueryKeepsCJK(t *testing.T) {
	got := SanitizeBM25Query("企业知识库平台的检索能力")
	if !strings.Contains(got, "企业知识库平台的检索能力") {
		t.Fatalf("expected CJK terms to survive sanitization, got %q", got)
	}
}

// Reserved words must not appear as terms, otherwise the joined expression
// would contain an operator the user injected.
func TestSanitizeBM25QueryResultIsOnlyTerms(t *testing.T) {
	expression := SanitizeBM25Query(`cat " OR " dog AND elephant`)
	for _, term := range strings.Split(expression, " OR ") {
		lowered := strings.ToLower(term)
		if lowered == "and" || lowered == "or" || lowered == "not" {
			t.Fatalf("reserved word leaked into the query: %q", expression)
		}
	}
}

// Scope filters must live in the recall SQL itself, and slice parameters must
// use the IN form GORM expands. A bare ANY(?) binding degrades into a single
// quoted value at runtime, so guard the shape here.
func TestRecallSQLPushesScopeFiltersWithExpandablePlaceholders(t *testing.T) {
	for _, query := range []string{bm25RecallQuery, parentChunksQuery, neighbourChunksQuery} {
		if strings.Contains(query, "ANY(?)") {
			t.Fatalf("recall queries must bind scope with IN ?, not ANY(?):\n%s", query)
		}
	}
	for _, fragment := range []string{"c.tenant_id = ?", "c.knowledge_base_id IN ?", "c.status = ?", "c.content @@@ ?", "d.latest_version_id = c.document_version_id"} {
		if !strings.Contains(bm25RecallQuery, fragment) {
			t.Fatalf("bm25 recall query is missing the scope filter %q", fragment)
		}
	}
	if !strings.Contains(parentChunksQuery, "c.tenant_id = ?") || !strings.Contains(parentChunksQuery, "c.knowledge_base_id IN ?") {
		t.Fatalf("parent expansion must reapply the scope filters")
	}
	if !strings.Contains(neighbourChunksQuery, "c.tenant_id = ?") {
		t.Fatalf("neighbour expansion must reapply the scope filters")
	}
}
