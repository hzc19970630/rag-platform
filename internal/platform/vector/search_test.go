package vector

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The vector recall query must inline the dimension as a type modifier (the
// HNSW index is partial on embedding_dimension) and bind the scope with the
// expandable IN form. A bare ANY(?) binding fails at runtime.
func TestVectorRecallTemplateScopeAndDimensionBinding(t *testing.T) {
	if strings.Contains(vectorRecallTemplate, "ANY(?)") {
		t.Fatalf("vector recall must bind the knowledge-base scope with IN ?, not ANY(?)")
	}
	for _, fragment := range []string{
		"FROM chunk_embeddings e",
		"JOIN chunks c ON c.id = ann.chunk_id",
		"e.tenant_id = ?",
		"e.knowledge_base_id IN (?)",
		"e.model = ?",
		"e.dimension = %d",
		"c.status = ?",
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

// Raw SQL does not expand a slice placeholder unless it is written as "(?)".
// A bare "IN ?" is sent as "IN $n", which PostgreSQL rejects; the vector
// channel then fails silently and only keyword recall grounds the answer.
func TestVectorRecallRawExpandsKnowledgeBaseSlice(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=x dbname=x sslmode=disable"}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	rendered := fmt.Sprintf(vectorRecallTemplate, 3, 3, 3, 3, 3)
	tx := db.Raw(rendered,
		"lit", "tenant-1", []string{"kb-1", "kb-2"}, "model-a",
		"lit", 200,
		"EVIDENCE_READY", "EVIDENCE_READY", 20)
	statement := tx.Statement.SQL.String()
	if strings.Contains(statement, "IN $") {
		t.Fatalf("knowledge-base scope was not expanded: %s", statement)
	}
	if !regexp.MustCompile(`IN \(\$\d+,\$\d+\)`).MatchString(statement) {
		t.Fatalf("knowledge-base scope must expand to two placeholders: %s", statement)
	}
	maxPlaceholder := 0
	for _, match := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(statement, -1) {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("parse placeholder %q: %v", match[0], err)
		}
		if value > maxPlaceholder {
			maxPlaceholder = value
		}
	}
	if maxPlaceholder != len(tx.Statement.Vars) {
		t.Fatalf("placeholder count %d does not match %d bound vars: %s", maxPlaceholder, len(tx.Statement.Vars), statement)
	}
}
