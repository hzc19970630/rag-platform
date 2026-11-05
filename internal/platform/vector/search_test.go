package vector

import (
	"fmt"
	"strings"
	"testing"
)

// The vector recall query must inline the dimension as a type modifier (the
// HNSW index is partial on embedding_dimension) and bind the scope with the
// expandable IN form. A bare ANY(?) binding fails at runtime.
func TestVectorRecallTemplateScopeAndDimensionBinding(t *testing.T) {
	if strings.Contains(vectorRecallTemplate, "ANY(?)") {
		t.Fatalf("vector recall must bind the knowledge-base scope with IN ?, not ANY(?)")
	}
	for _, fragment := range []string{
		"c.tenant_id = ?",
		"c.knowledge_base_id IN ?",
		"c.status = ?",
		"c.embedding_dimension = %d",
		"c.embedding_model = ?",
		"c.embedding IS NOT NULL",
		"d.latest_version_id = c.document_version_id",
	} {
		if !strings.Contains(vectorRecallTemplate, fragment) {
			t.Fatalf("vector recall query is missing %q", fragment)
		}
	}
	rendered := fmt.Sprintf(vectorRecallTemplate, 3, 3, 3, 3, 3)
	if strings.Contains(rendered, "%d") {
		t.Fatalf("the rendered query must not keep printf verbs: %s", rendered)
	}
	if !strings.Contains(rendered, "halfvec(3)") {
		t.Fatalf("the rendered query must cast to halfvec(3): %s", rendered)
	}
	if !strings.Contains(rendered, "LIMIT ?") {
		t.Fatalf("the rendered query must keep the bound LIMIT: %s", rendered)
	}
}

func TestVectorScanLimitAppliesOversampleAndCap(t *testing.T) {
	cases := []struct {
		topK, oversample, want int
	}{
		{20, 1, 20},
		{20, 4, 80},
		{0, 4, 0},
		{20, 0, 20},
		{500, 20, maxVectorLimit},
	}
	for _, testCase := range cases {
		if got := vectorScanLimit(testCase.topK, testCase.oversample); got != testCase.want {
			t.Fatalf("vectorScanLimit(%d, %d) = %d, want %d", testCase.topK, testCase.oversample, got, testCase.want)
		}
	}
}

func TestVectorEFSearchStatementRendersValidatedInt(t *testing.T) {
	if got := fmt.Sprintf(vectorEFSearchStatement, 200); got != "SET LOCAL hnsw.ef_search = 200" {
		t.Fatalf("unexpected ef_search statement: %q", got)
	}
}
