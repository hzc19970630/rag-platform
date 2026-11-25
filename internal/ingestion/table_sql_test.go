package ingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type scriptedChatter struct {
	reply  string
	err    error
	last   model.ChatRequest
	called int
}

func (c *scriptedChatter) Complete(_ context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	c.called++
	c.last = request
	if c.err != nil {
		return model.ChatResponse{}, c.err
	}
	return model.ChatResponse{Text: c.reply, Model: "fake"}, nil
}

func (c *scriptedChatter) Name() string { return "fake" }

func newTableSQLFixture(t *testing.T, chatter *scriptedChatter) (*Service, *gorm.DB) {
	t.Helper()
	service, db, _ := newReparseFixture(t)
	seedTableAsset(t, db, "asset-orders", "Sheet1", []string{"订单号", "客户", "数量", "金额"}, []map[string]string{
		{"订单号": "A-100", "客户": "张三", "数量": "2", "金额": "10"},
		{"订单号": "A-101", "客户": "李四", "数量": "5", "金额": "30"},
		{"订单号": "A-102", "客户": "张三", "数量": "3", "金额": "20"},
	})
	service.SetTableAskSQLOptions(TableAskSQLOptions{
		Chat: chatter,
		RenderPrompt: func(schema, question string) (string, error) {
			return "schema:\n" + schema + "\nquestion:" + question, nil
		},
		MinConfidence: 0.7, MaxRows: 50, Timeout: 5 * time.Second,
	})
	return service, db
}

func TestAskTableSQLPreviewAndExecute(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"sum","column":"数量","filters":[{"column":"客户","op":"eq","value":"张三"}],"confidence":0.92}`}
	service, db := newTableSQLFixture(t, chatter)
	principal := ownerPrincipal()
	ctx := context.Background()

	preview, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{
		Question: "张三的数量总和是多少", AssetID: "asset-orders", Preview: true,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Preview || preview.Plan == nil || preview.Result != nil {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.Plan.Column != "数量" || len(preview.Plan.Filters) != 1 || preview.Plan.Filters[0].Column != "客户" {
		t.Fatalf("plan = %+v", preview.Plan)
	}
	if preview.EstimatedRows != 2 || !strings.Contains(preview.SQL, "table_rows") || !strings.Contains(strings.ToUpper(preview.SQL), "SELECT") {
		t.Fatalf("preview sql/rows = %q / %d", preview.SQL, preview.EstimatedRows)
	}
	if chatter.called != 1 || chatter.last.Model != "" {
		t.Fatalf("chatter = %+v", chatter)
	}
	assertAuditAction(t, db, "table_query_preview", 1)

	executed, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{
		Question: "张三的数量总和是多少", AssetID: "asset-orders",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if executed.Fallback || executed.Result == nil || executed.Result.Value == nil || *executed.Result.Value != 5 {
		t.Fatalf("execute = %+v", executed)
	}
	if executed.MatchedRows != 2 || len(executed.Result.Rows) != 2 {
		t.Fatalf("matched = %d rows=%d", executed.MatchedRows, len(executed.Result.Rows))
	}
	assertAuditAction(t, db, "table_query_execute", 1)
}

func TestAskTableSQLFallsBackAndRejectsUnsafePlans(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"sum","column":"数量","confidence":0.1}`}
	service, _ := newTableSQLFixture(t, chatter)
	principal := ownerPrincipal()
	ctx := context.Background()

	low, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("low confidence: %v", err)
	}
	if !low.Fallback || low.FallbackReason != "low_confidence" || low.Result == nil || low.Result.Value == nil || *low.Result.Value != 10 {
		t.Fatalf("low confidence result = %+v", low)
	}
	preview, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{
		Question: "各订单的数量总和是多少", AssetID: "asset-orders", Preview: true,
	})
	if err != nil || !preview.Fallback || preview.Result != nil {
		t.Fatalf("low confidence preview = %+v, %v", preview, err)
	}

	chatter.reply = `{"operation":"sum","column":"不存在","confidence":0.9}`
	invalid, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil || !invalid.Fallback || !strings.Contains(invalid.FallbackReason, "plan_invalid") {
		t.Fatalf("invalid column = %+v, %v", invalid, err)
	}

	chatter.reply = `{"operation":"delete","confidence":0.9}`
	unsupported, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil || !unsupported.Fallback || !strings.Contains(unsupported.FallbackReason, "plan_invalid") {
		t.Fatalf("unsupported operation = %+v, %v", unsupported, err)
	}

	chatter.reply = `{"operation":"sum","column":"数量","confidence":0.9}`
	notAggregate, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "张三的订单在哪里", AssetID: "asset-orders"})
	if err != nil || !notAggregate.Fallback || !strings.Contains(notAggregate.FallbackReason, "route=lookup") {
		t.Fatalf("non-aggregate route = %+v, %v", notAggregate, err)
	}
}

func TestAskTableSQLContainsFilter(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"sum","column":"数量","filters":[{"column":"客户","op":"contains","value":"张"}],"confidence":0.9}`}
	service, _ := newTableSQLFixture(t, chatter)
	result, err := service.AskTableSQL(context.Background(), ownerPrincipal(), "kb-a", TableAskSQLInput{
		Question: "客户包含张的数量总和是多少", AssetID: "asset-orders",
	})
	if err != nil {
		t.Fatalf("contains filter: %v", err)
	}
	if result.Fallback || result.Result == nil || result.Result.Value == nil || *result.Result.Value != 5 {
		t.Fatalf("contains result = %+v", result)
	}
}

func TestAskTableSQLTreatsFilterValuesAsLiterals(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"count","filters":[{"column":"客户","op":"eq","value":"x' OR 1=1 --"}],"confidence":0.9}`}
	service, _ := newTableSQLFixture(t, chatter)
	principal := ownerPrincipal()
	ctx := context.Background()

	result, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "有多少条订单记录", AssetID: "asset-orders"})
	if err != nil {
		t.Fatalf("injection filter: %v", err)
	}
	if result.Result == nil || result.Result.Value == nil || *result.Result.Value != 0 {
		t.Fatalf("literal filter should match no rows: %+v", result.Result)
	}
}

func TestAskTableSQLRowLimitFallsBack(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"sum","column":"数量","confidence":0.9}`}
	service, db := newTableSQLFixture(t, chatter)
	principal := ownerPrincipal()
	ctx := context.Background()

	rows := make([]map[string]string, 0, 60)
	for index := 0; index < 60; index++ {
		rows = append(rows, map[string]string{"订单号": "B-" + string(rune('A'+index%26)), "客户": "客户", "数量": "1", "金额": "1"})
	}
	seedTableAsset(t, db, "asset-big", "Sheet2", []string{"订单号", "客户", "数量", "金额"}, rows)
	result, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "Sheet2 的数量总和是多少"})
	// Sheet2 selection needs the exact sheet name; both assets exist, so pass it.
	if err != nil {
		t.Fatalf("row limit: %v", err)
	}
	if !result.Fallback || !strings.Contains(result.FallbackReason, "row_limit_exceeded") || result.Result == nil || result.Result.Value == nil || *result.Result.Value != 60 {
		t.Fatalf("row limit result = %+v", result)
	}
}

func assertAuditAction(t *testing.T, db *gorm.DB, action string, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&models.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil {
		t.Fatalf("count audit %s: %v", action, err)
	}
	if count != want {
		t.Fatalf("audit %s count = %d, want %d", action, count, want)
	}
}
