package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func TestAskTableRoutesAndAnswers(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	seedTableAsset(t, db, "asset-orders", "Sheet1", []string{"订单号", "客户", "数量", "金额"}, []map[string]string{
		{"订单号": "A-100", "客户": "张三", "数量": "2", "金额": "10"},
		{"订单号": "A-101", "客户": "李四", "数量": "5", "金额": "30"},
		{"订单号": "A-102", "客户": "张三", "数量": "3", "金额": "20"},
		{"订单号": "A-103", "客户": "王五", "数量": "n/a", "金额": "5"},
	})
	seedTableAsset(t, db, "asset-sheet2", "Sheet2", []string{"订单号", "客户", "数量", "金额"}, []map[string]string{
		{"订单号": "B-200", "客户": "赵六", "数量": "7", "金额": "70"},
	})

	lookup, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "张三的订单在哪里", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if lookup.Route != "lookup" || lookup.MatchedRows != 2 || len(lookup.Rows) != 2 {
		t.Fatalf("lookup = %+v", lookup)
	}
	if lookup.Rows[0].Cells["客户"] != "张三" {
		t.Fatalf("lookup rows = %+v", lookup.Rows)
	}

	sum, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if sum.Operation != "sum" || sum.Column != "数量" || sum.Value == nil || *sum.Value != 10 || sum.MatchedRows != 3 {
		t.Fatalf("sum = %+v", sum)
	}

	avg, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "平均金额是多少", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("avg: %v", err)
	}
	if avg.Value == nil || *avg.Value != 16.25 {
		t.Fatalf("avg = %+v", avg)
	}

	max, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "最高的金额是多少", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("max: %v", err)
	}
	if max.Operation != "max" || max.Value == nil || *max.Value != 30 {
		t.Fatalf("max = %+v", max)
	}

	top, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "金额最高的前2条", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if top.Operation != "top" || top.TopN != 2 || len(top.Rows) != 2 {
		t.Fatalf("top = %+v", top)
	}
	if top.Rows[0].Cells["订单号"] != "A-101" || top.Rows[1].Cells["订单号"] != "A-102" {
		t.Fatalf("top rows = %+v", top.Rows)
	}

	count, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "一共有多少行", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count.Operation != "count" || count.Value == nil || *count.Value != 4 {
		t.Fatalf("count = %+v", count)
	}

	explain, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "这张表有哪些列", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if explain.Route != "explain" || len(explain.Header) != 4 || len(explain.Rows) != 0 {
		t.Fatalf("explain = %+v", explain)
	}
}

func TestAskTableSelectsAndRejectsAssets(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	seedTableAsset(t, db, "asset-a", "Sheet1", []string{"订单号", "数量"}, []map[string]string{{"订单号": "A", "数量": "1"}})
	seedTableAsset(t, db, "asset-b", "Sheet2", []string{"订单号", "数量"}, []map[string]string{{"订单号": "B", "数量": "2"}})
	if err := db.Create(&models.TableAsset{
		ID: "asset-stale", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-0",
		Sheet: "Sheet1", Header: `["订单号","数量"]`, RowCount: 1, StorageMode: StorageModeFull,
	}).Error; err != nil {
		t.Fatalf("seed stale asset: %v", err)
	}

	selected, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "Sheet1 一共有多少行"})
	if err != nil {
		t.Fatalf("select by sheet: %v", err)
	}
	if selected.AssetID != "asset-a" {
		t.Fatalf("selected = %+v", selected)
	}
	bySheet, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "Sheet2 一共有多少行"})
	if err != nil {
		t.Fatalf("select by second sheet: %v", err)
	}
	if bySheet.AssetID != "asset-b" {
		t.Fatalf("selected second sheet = %+v", bySheet)
	}

	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "一共有多少行"}); err == nil || !strings.Contains(err.Error(), "table_asset_required") {
		t.Fatalf("unqualified question error = %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "数量总和是多少"}); err == nil || !strings.Contains(err.Error(), "table_asset_ambiguous") {
		t.Fatalf("ambiguous question error = %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-other", TableAskInput{Question: "Sheet1 有多少行"}); err == nil {
		t.Fatalf("cross-knowledge-base question must fail")
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "总和是多少", AssetID: "asset-a"}); err == nil || !strings.Contains(err.Error(), "table_column_required") {
		t.Fatalf("missing column error = %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "客户总和是多少", AssetID: "asset-a"}); err == nil || !strings.Contains(err.Error(), "table_column_required") && !strings.Contains(err.Error(), "table_column_not_numeric") {
		t.Fatalf("non-numeric column error = %v", err)
	}

	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: strings.Repeat("表", 1001), AssetID: "asset-a"}); err == nil || !strings.Contains(err.Error(), "table_question_too_long") {
		t.Fatalf("long question error = %v", err)
	}
	if err := db.Create(&models.TableAsset{
		ID: "asset-sampled", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "huge", Header: `["名称","数量"]`, RowCount: 10, TotalRows: 500000, ParsedRows: 50,
		StorageMode: StorageModeSampled, ParseStatus: ParseStatusSampled, Truncated: true,
		SampleRows: `["名称 | 数量","a | 1"]`,
	}).Error; err != nil {
		t.Fatalf("seed sampled asset: %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "数量总和是多少", AssetID: "asset-sampled"}); err == nil || !strings.Contains(err.Error(), "table_rows_not_indexed") {
		t.Fatalf("sampled aggregate error = %v", err)
	}
	explain, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "这张表有哪些列", AssetID: "asset-sampled"})
	if err != nil {
		t.Fatalf("sampled explain: %v", err)
	}
	if explain.Route != "explain" || len(explain.Sample) != 2 || len(explain.Notes) == 0 {
		t.Fatalf("sampled explain = %+v", explain)
	}
}

func seedTableAsset(t *testing.T, db *gorm.DB, assetID, sheet string, header []string, rows []map[string]string) {
	t.Helper()
	headerJSON, _ := json.Marshal(header)
	asset := &models.TableAsset{
		ID: assetID, TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: sheet, Header: string(headerJSON), RowCount: len(rows), ColumnCount: len(header),
		StorageMode: StorageModeFull, ParseStatus: ParseStatusParsed,
	}
	if err := db.Create(asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	for index, cells := range rows {
		encoded, _ := json.Marshal(cells)
		row := &models.TableRow{
			ID: assetID + "-row-" + string(rune('a'+index)), TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentID: "doc-a", DocumentVersionID: "ver-1", TableAssetID: assetID,
			RowNumber: index + 2, Cells: string(encoded),
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
}
