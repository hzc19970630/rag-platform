package ingestion

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestQueryTableFiltersAndSorts(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	if err := db.Create(&models.TableAsset{
		ID: "asset-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name","qty"]`, RowCount: 3, ColumnCount: 2,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	rows := []map[string]string{
		{"name": "pen", "qty": "2"},
		{"name": "tape", "qty": "5"},
		{"name": "box", "qty": "3"},
	}
	for index, cells := range rows {
		encoded, _ := json.Marshal(cells)
		if err := db.Create(&models.TableRow{
			ID: "row-" + string(rune('a'+index)), TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentID: "doc-a", DocumentVersionID: "ver-1", TableAssetID: "asset-1",
			RowNumber: index + 2, Cells: string(encoded),
		}).Error; err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
	result, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{
		AssetID: "asset-1", Filters: []TableFilter{{Column: "qty", Op: "gte", Value: "3"}},
		OrderBy: "qty", Desc: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if result.MatchedRows != 2 || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Rows[0].Cells["name"] != "tape" || result.Rows[1].Cells["name"] != "box" {
		t.Fatalf("rows = %+v", result.Rows)
	}
	if result.Header[0] != "name" || result.Header[1] != "qty" {
		t.Fatalf("header = %v", result.Header)
	}
	limited, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-1", Limit: 1})
	if err != nil || len(limited.Rows) != 1 || limited.MatchedRows != 3 {
		t.Fatalf("limited = %+v, %v", limited, err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-other", TableQuery{AssetID: "asset-1"}); err == nil {
		t.Fatalf("cross-knowledge-base query must fail")
	}
}
