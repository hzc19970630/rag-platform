package ingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
)

type scriptedChatter struct {
	reply  string
	err    error
	called int
	last   model.ChatRequest
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

func newTableSQLFixture(t *testing.T, chatter *scriptedChatter) (*Service, *fakeColumnarReader) {
	t.Helper()
	service, reader := newTableAskFixture(t)
	service.SetTableAskSQLOptions(TableAskSQLOptions{
		Chat: chatter,
		RenderPrompt: func(schema, question string) (string, error) {
			return "schema:\n" + schema + "\nquestion:" + question, nil
		},
		MinConfidence: 0.7, MaxRows: 50, Timeout: 5 * time.Second,
	})
	return service, reader
}

func TestAskTableSQLPreviewAndExecute(t *testing.T) {
	chatter := &scriptedChatter{reply: `{"operation":"sum","column":"数量","filters":[{"column":"客户","op":"eq","value":"张三"}],"confidence":0.92}`}
	service, _ := newTableSQLFixture(t, chatter)
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
	if preview.EstimatedRows != 2 || !strings.Contains(strings.ToUpper(preview.SQL), "SELECT") || !strings.Contains(preview.SQL, "TRY_CAST") {
		t.Fatalf("preview sql/rows = %q / %d", preview.SQL, preview.EstimatedRows)
	}
	if chatter.called != 1 {
		t.Fatalf("chatter = %+v", chatter)
	}

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
	if !low.Fallback || low.FallbackReason != "low_confidence" || low.Result == nil || low.Result.Value == nil || *low.Result.Value != 5 {
		t.Fatalf("low confidence = %+v", low)
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
		t.Fatalf("non-aggregate = %+v, %v", notAggregate, err)
	}

	service.SetTableAskSQLOptions(TableAskSQLOptions{})
	noPlanner, err := service.AskTableSQL(ctx, principal, "kb-a", TableAskSQLInput{Question: "各订单的数量总和是多少", AssetID: "asset-orders"})
	if err != nil || !noPlanner.Fallback || noPlanner.FallbackReason != "sql_planner_unavailable" {
		t.Fatalf("no planner = %+v, %v", noPlanner, err)
	}
}
