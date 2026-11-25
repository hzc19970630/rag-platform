package tableqa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEvaluationCasesRouteAsExpected keeps the committed B37 question set in
// step with the rule engine. A routing change must update the dataset or the
// dataset must justify the new route.
func TestEvaluationCasesRouteAsExpected(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "eval", "b37_tableqa", "cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	var document struct {
		Cases []struct {
			ID        string  `json:"id"`
			Question  string  `json:"question"`
			Route     string  `json:"route"`
			Operation *string `json:"operation"`
			Expect    struct {
				Column *string `json:"column"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode cases: %v", err)
	}
	if len(document.Cases) == 0 {
		t.Fatal("no evaluation cases")
	}
	headerByQuestion := map[string][]string{
		"订单 A-0100 的金额是多少": {"订单号", "客户", "数量", "金额", "状态"},
		"商品-0150 在哪个仓库":    {"商品", "仓库", "库存量", "单价"},
		"订单一共有多少行":         {"订单号", "客户", "数量", "金额", "状态"},
		"订单的数量总和是多少":       {"订单号", "客户", "数量", "金额", "状态"},
		"订单的平均金额是多少":       {"订单号", "客户", "数量", "金额", "状态"},
		"订单最高的金额是多少":       {"订单号", "客户", "数量", "金额", "状态"},
		"订单金额最高的前3条":       {"订单号", "客户", "数量", "金额", "状态"},
		"库存量最低的3个商品":       {"商品", "仓库", "库存量", "单价"},
		"订单这张表有哪些列":        {"订单号", "客户", "数量", "金额", "状态"},
		"库存表是什么":           {"商品", "仓库", "库存量", "单价"},
	}
	for _, testCase := range document.Cases {
		header := headerByQuestion[testCase.Question]
		if header == nil {
			t.Fatalf("%s: no header registered for %q", testCase.ID, testCase.Question)
		}
		result := Resolve(testCase.Question, header)
		if result.Route != testCase.Route {
			t.Fatalf("%s: route = %s, want %s (%v)", testCase.ID, result.Route, testCase.Route, result.MatchedRules)
		}
		if testCase.Operation != nil && result.Operation != *testCase.Operation {
			t.Fatalf("%s: operation = %s, want %s (%v)", testCase.ID, result.Operation, *testCase.Operation, result.MatchedRules)
		}
		if testCase.Expect.Column != nil && result.Column != *testCase.Expect.Column {
			t.Fatalf("%s: column = %q, want %q (%v)", testCase.ID, result.Column, *testCase.Expect.Column, result.MatchedRules)
		}
	}
}
