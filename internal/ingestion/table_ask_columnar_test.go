package ingestion

import (
	"context"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type fakeColumnarReader struct {
	last []string
}

func (f *fakeColumnarReader) QueryTable(context.Context, TableMaterializeTarget, string, ColumnarTableQuery) (ColumnarTableResult, error) {
	return ColumnarTableResult{}, nil
}

func (f *fakeColumnarReader) RunTableSQL(_ context.Context, _ TableMaterializeTarget, _ string, query ColumnarSQLQuery) (ColumnarSQLResult, error) {
	f.last = append(f.last, query.SQL)
	upper := strings.ToUpper(query.SQL)
	switch {
	case strings.Contains(upper, "COUNT(*)"):
		return ColumnarSQLResult{Columns: []TableSQLColumn{{Name: "value", Type: "BIGINT"}}, Rows: [][]any{{float64(2)}}, RowCount: 1, ReturnedRows: 1}, nil
	case strings.Contains(upper, "SUM("):
		return ColumnarSQLResult{Columns: []TableSQLColumn{{Name: "value", Type: "DOUBLE"}}, Rows: [][]any{{float64(5)}}, RowCount: 1, ReturnedRows: 1}, nil
	default:
		return ColumnarSQLResult{
			Columns:  []TableSQLColumn{{Name: "订单号", Type: "VARCHAR"}, {Name: "客户", Type: "VARCHAR"}, {Name: "数量", Type: "BIGINT"}, {Name: "金额", Type: "BIGINT"}, {Name: "__row_number", Type: "BIGINT"}},
			Rows:     [][]any{{"A-100", "张三", int64(2), int64(10), int64(2)}},
			RowCount: 1, ReturnedRows: 1,
		}, nil
	}
}

func TestCompileColumnarPlan(t *testing.T) {
	header := []string{"订单号", "客户", "数量"}
	compiled, err := compileColumnarPlan(header, TableAskSQLPlan{
		Operation: "sum", Column: "数量", Confidence: 0.9,
		Filters: []TableFilter{{Column: "客户", Op: "eq", Value: "张三"}},
	})
	if err != nil {
		t.Fatalf("compile sum: %v", err)
	}
	if !strings.Contains(compiled.SQL, `SUM(TRY_CAST("数量" AS DOUBLE))`) || !strings.Contains(compiled.SQL, `CAST("客户" AS VARCHAR) = ?`) {
		t.Fatalf("sum sql = %s", compiled.SQL)
	}
	if len(compiled.Params) != 1 || compiled.Params[0] != "张三" || compiled.Limit != 1 {
		t.Fatalf("sum params = %+v", compiled)
	}
	if _, err := compileColumnarPlan(header, TableAskSQLPlan{Operation: "sum", Column: "缺失", Confidence: 0.9}); err == nil {
		t.Fatal("unknown column must fail")
	}
}

func TestColumnarRowsMapHeaderAndRowNumber(t *testing.T) {
	header := []string{"订单号", "客户", "数量"}
	result := TableSQLResult{
		Columns: []TableSQLColumn{{Name: "订单号", Type: "VARCHAR"}, {Name: "客户", Type: "VARCHAR"}, {Name: "数量", Type: "BIGINT"}, {Name: "__row_number", Type: "BIGINT"}},
		Rows:    [][]any{{"A-100", "张三", int64(2), int64(7)}},
	}
	rows := columnarRows(header, result)
	if len(rows) != 1 || rows[0].RowNumber != 7 || rows[0].Cells["客户"] != "张三" || rows[0].Cells["数量"] != "2" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAskTableUsesColumnarCopyForB39Assets(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"sum","column":"数量","confidence":0.9}`}
	service, db := newTableSQLFixture(t, chatter)
	if err := db.Model(&models.TableAsset{}).Where("id = ?", "asset-orders").
		Updates(map[string]any{"storage_uri": "s3://bucket/tablehouse/asset-orders", "materialize_status": MaterializeStatusReady}).Error; err != nil {
		t.Fatalf("mark columnar: %v", err)
	}
	reader := &fakeColumnarReader{}
	service.SetTablehouse(reader)
	principal := ownerPrincipal()
	ctx := context.Background()

	answer, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("columnar ask: %v", err)
	}
	if answer.Value == nil || *answer.Value != 5 || answer.MatchedRows != 2 {
		t.Fatalf("columnar answer = %+v", answer)
	}
	sumSeen := false
	for _, call := range reader.last {
		if strings.Contains(strings.ToUpper(call), "SUM(") {
			sumSeen = true
		}
	}
	if !sumSeen {
		t.Fatalf("reader calls = %v", reader.last)
	}

	planned, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("columnar planned ask: %v", err)
	}
	if planned.Result == nil || planned.Result.Value == nil || *planned.Result.Value != 5 {
		t.Fatalf("planned = %+v", planned)
	}
}
