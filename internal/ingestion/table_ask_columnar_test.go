package ingestion

import (
	"context"
	"strings"
	"testing"
)

type fakeColumnarReader struct {
	calls []string
}

func (f *fakeColumnarReader) QueryTable(context.Context, TableMaterializeTarget, string, ColumnarTableQuery) (ColumnarTableResult, error) {
	return ColumnarTableResult{}, nil
}

func (f *fakeColumnarReader) RunTableSQL(_ context.Context, _ TableMaterializeTarget, sheet string, query ColumnarSQLQuery) (ColumnarSQLResult, error) {
	f.calls = append(f.calls, query.SQL)
	upper := strings.ToUpper(query.SQL)
	columns := []TableSQLColumn{{Name: "订单号", Type: "VARCHAR"}, {Name: "客户", Type: "VARCHAR"}, {Name: "数量", Type: "BIGINT"}, {Name: "金额", Type: "BIGINT"}, {Name: "__row_number", Type: "BIGINT"}}
	if sheet == "库存" {
		columns = []TableSQLColumn{{Name: "商品", Type: "VARCHAR"}, {Name: "仓库", Type: "VARCHAR"}, {Name: "库存量", Type: "BIGINT"}, {Name: "单价", Type: "BIGINT"}, {Name: "__row_number", Type: "BIGINT"}}
	}
	rows := [][]any{{"A-100", "张三", int64(2), int64(10), int64(2)}, {"A-102", "张三", int64(3), int64(20), int64(4)}}
	if sheet == "库存" {
		rows = [][]any{{"商品-0150", "南仓", int64(150), int64(1), int64(151)}}
	}
	switch {
	case strings.Contains(upper, "COUNT(*)"):
		return ColumnarSQLResult{Columns: []TableSQLColumn{{Name: "value", Type: "BIGINT"}}, Rows: [][]any{{float64(len(rows))}}, RowCount: 1, ReturnedRows: 1}, nil
	case strings.Contains(upper, "SUM("):
		return ColumnarSQLResult{Columns: []TableSQLColumn{{Name: "value", Type: "DOUBLE"}}, Rows: [][]any{{float64(5)}}, RowCount: 1, ReturnedRows: 1}, nil
	case strings.Contains(upper, "ORDER BY TRY_CAST") && strings.Contains(upper, "DESC"):
		return ColumnarSQLResult{Columns: columns, Rows: [][]any{rows[1], rows[0]}, RowCount: len(rows), ReturnedRows: len(rows)}, nil
	default:
		return ColumnarSQLResult{Columns: columns, Rows: rows, RowCount: len(rows), ReturnedRows: len(rows)}, nil
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
