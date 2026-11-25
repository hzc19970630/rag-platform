package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func seedColumnarAsset(t *testing.T, db *gorm.DB, assetID, sheet string, header []string, rows int64) {
	t.Helper()
	headerJSON, _ := json.Marshal(header)
	asset := &models.TableAsset{
		ID: assetID, TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: sheet, Header: string(headerJSON), RowCount: int(rows), ColumnCount: len(header),
		StorageMode: StorageModeFull, ParseStatus: ParseStatusParsed,
		StorageURI: "s3://bucket/tablehouse/" + assetID, MaterializeStatus: MaterializeStatusReady,
		ParsedRows: rows, TotalRows: rows, MaterializedRows: rows,
	}
	if err := db.Create(asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
}

func newTableAskFixture(t *testing.T) (*Service, *fakeColumnarReader) {
	t.Helper()
	service, db, _ := newReparseFixture(t)
	seedColumnarAsset(t, db, "asset-orders", "订单", []string{"订单号", "客户", "数量", "金额"}, 300)
	seedColumnarAsset(t, db, "asset-inventory", "库存", []string{"商品", "仓库", "库存量", "单价"}, 200)
	reader := &fakeColumnarReader{}
	service.SetTablehouse(reader)
	return service, reader
}

func TestAskTableRoutesOverColumnarCopy(t *testing.T) {
	service, _ := newTableAskFixture(t)
	principal := ownerPrincipal()
	ctx := context.Background()

	lookup, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "张三的订单在哪里"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if lookup.Route != "lookup" || lookup.MatchedRows != 2 || len(lookup.Rows) != 2 {
		t.Fatalf("lookup = %+v", lookup)
	}
	if lookup.Rows[0].Cells["客户"] != "张三" || lookup.Rows[0].RowNumber == 0 {
		t.Fatalf("lookup rows = %+v", lookup.Rows)
	}

	sum, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "各订单的数量总和是多少"})
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if sum.Operation != "sum" || sum.Column != "数量" || sum.Value == nil || *sum.Value != 5 || sum.MatchedRows != 2 {
		t.Fatalf("sum = %+v", sum)
	}
	if len(sum.Rows) == 0 {
		t.Fatal("aggregate must keep sample rows for citations")
	}

	top, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "订单金额最高的前2条"})
	if err != nil {
		t.Fatalf("top: %v", err)
	}
	if top.Operation != "top" || len(top.Rows) != 2 || top.Rows[0].Cells["订单号"] != "A-102" {
		t.Fatalf("top = %+v", top)
	}

	count, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "订单一共有多少行"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count.Operation != "count" || count.Value == nil || *count.Value != 2 {
		t.Fatalf("count = %+v", count)
	}

	explain, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "订单这张表有哪些列"})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if explain.Route != "explain" || len(explain.Header) != 4 || len(explain.Rows) != 0 {
		t.Fatalf("explain = %+v", explain)
	}
}

func TestAskTableSelectsAssetsAndRejectsUnavailableCopies(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	seedColumnarAsset(t, db, "asset-a", "Sheet1", []string{"订单号", "数量"}, 1)
	seedColumnarAsset(t, db, "asset-b", "Sheet2", []string{"订单号", "数量"}, 1)
	service.SetTablehouse(&fakeColumnarReader{})
	principal := ownerPrincipal()
	ctx := context.Background()

	selected, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "Sheet2 一共有多少行"})
	if err != nil {
		t.Fatalf("select by sheet: %v", err)
	}
	if selected.AssetID != "asset-b" {
		t.Fatalf("selected = %+v", selected)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "数量总和是多少"}); err == nil || !strings.Contains(err.Error(), "table_asset_ambiguous") {
		t.Fatalf("ambiguous error = %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "一共有多少行"}); err == nil || !strings.Contains(err.Error(), "table_asset_required") {
		t.Fatalf("unqualified error = %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-other", TableAskInput{Question: "Sheet1 有多少行"}); err == nil {
		t.Fatalf("cross-knowledge-base question must fail")
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "总和是多少", AssetID: "asset-a"}); err == nil || !strings.Contains(err.Error(), "table_column_required") {
		t.Fatalf("missing column error = %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: strings.Repeat("表", 1001), AssetID: "asset-a"}); err == nil || !strings.Contains(err.Error(), "table_question_too_long") {
		t.Fatalf("long question error = %v", err)
	}

	if err := db.Model(&models.TableAsset{}).Where("id = ?", "asset-a").Updates(map[string]any{"storage_uri": "", "materialize_status": ""}).Error; err != nil {
		t.Fatalf("clear columnar copy: %v", err)
	}
	if _, err := service.AskTable(ctx, principal, "kb-a", TableAskInput{Question: "Sheet1 一共有多少行"}); err == nil || !strings.Contains(err.Error(), "table_needs_reparse") {
		t.Fatalf("missing copy error = %v", err)
	}
}
