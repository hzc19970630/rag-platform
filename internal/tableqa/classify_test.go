package tableqa

import (
	"context"
	"testing"
)

func TestClassifyRoutes(t *testing.T) {
	cases := []struct {
		question  string
		route     string
		operation string
		column    string
		topN      int
		desc      bool
	}{
		{"这张表有哪些列", RouteExplain, "", "", 0, false},
		{"介绍一下这张表", RouteExplain, "", "", 0, false},
		{"各产品的数量总和是多少", RouteAggregate, OpSum, "数量", 0, false},
		{"金额最高的前5个订单", RouteAggregate, OpTop, "金额", 5, true},
		{"最低的3条记录", RouteAggregate, OpTop, "", 3, false},
		{"数量最多的前十个", RouteAggregate, OpTop, "数量", 10, true},
		{"一共有多少行", RouteAggregate, OpCount, "", 0, false},
		{"客户A的订单在哪里", RouteLookup, OpRows, "", 0, false},
		{"帮我找一下编号 A-100 的记录", RouteLookup, OpRows, "", 0, false},
		{"top 10 amount", RouteAggregate, OpTop, "amount", 10, true},
	}
	for _, testCase := range cases {
		header := []string{"订单号", "客户", "数量", "金额", "amount"}
		result := Resolve(testCase.question, header)
		if result.Route != testCase.route || result.Operation != testCase.operation {
			t.Fatalf("%q route = %s/%s, want %s/%s (%v)", testCase.question, result.Route, result.Operation, testCase.route, testCase.operation, result.MatchedRules)
		}
		if result.Column != testCase.column {
			t.Fatalf("%q column = %q, want %q (%v)", testCase.question, result.Column, testCase.column, result.MatchedRules)
		}
		if result.TopN != testCase.topN || result.Desc != testCase.desc {
			t.Fatalf("%q topN/desc = %d/%t, want %d/%t (%v)", testCase.question, result.TopN, result.Desc, testCase.topN, testCase.desc, result.MatchedRules)
		}
	}
}

func TestResolveNeedsColumn(t *testing.T) {
	result := Resolve("总金额是多少", []string{"订单号", "客户"})
	if result.Route != RouteAggregate || result.Operation != OpSum {
		t.Fatalf("result = %+v", result)
	}
	if !result.NeedsColumn {
		t.Fatalf("expected NeedsColumn when the question names no header column: %+v", result)
	}
	count := Resolve("一共有多少行", []string{"订单号"})
	if count.NeedsColumn {
		t.Fatalf("count does not need a column: %+v", count)
	}
}

func TestMatchColumnsPrefersLongestAlias(t *testing.T) {
	columns := MatchColumns("请给出总金额最高的行", []string{"金额（元）", "金额", "数量"})
	if len(columns) != 2 || columns[0] != "金额（元）" || columns[1] != "金额" {
		t.Fatalf("columns = %v", columns)
	}
	if Chinese := MatchColumns("ID是什么", []string{"id", "名称"}); len(Chinese) != 0 {
		t.Fatalf("short ascii aliases must not match: %v", Chinese)
	}
}

func TestTokensKeepLookupTerms(t *testing.T) {
	tokens := Tokens("客户的订单在哪里")
	set := map[string]bool{}
	for _, token := range tokens {
		set[token] = true
	}
	for _, want := range []string{"客户", "订单"} {
		if !set[want] {
			t.Fatalf("tokens %v missing %q", tokens, want)
		}
	}
	if set["的"] {
		t.Fatalf("stop runes leaked into tokens: %v", tokens)
	}
}

func TestParseNumber(t *testing.T) {
	cases := map[string]float64{
		"1,234.5": 1234.5,
		"￥2,000":  2000,
		"(300)":   -300,
		"12%":     12,
		" 42 元":   42,
	}
	for raw, want := range cases {
		value, ok := ParseNumber(raw)
		if !ok || value != want {
			t.Fatalf("ParseNumber(%q) = %v/%t, want %v", raw, value, ok, want)
		}
	}
	for _, raw := range []string{"", "-", "N/A", "abc"} {
		if _, ok := ParseNumber(raw); ok {
			t.Fatalf("ParseNumber(%q) must fail", raw)
		}
	}
}

func TestClassifyWithFallback(t *testing.T) {
	called := 0
	fallback := func(context.Context, string) (Classification, error) {
		called++
		return Classification{Route: RouteAggregate, Operation: OpCount, Confidence: 0.7}, nil
	}
	known := ClassifyWithFallback(context.Background(), "这张表有哪些列", fallback)
	if called != 0 || known.Fallback {
		t.Fatalf("high-confidence rules must not call the fallback: %+v", known)
	}
	unknown := ClassifyWithFallback(context.Background(), "嗯嗯", fallback)
	if called != 1 || !unknown.Fallback || unknown.Route != RouteAggregate {
		t.Fatalf("low-confidence question should use the fallback: %+v", unknown)
	}
	failing := ClassifyWithFallback(context.Background(), "嗯嗯", func(context.Context, string) (Classification, error) {
		return Classification{}, context.Canceled
	})
	if failing.Route != RouteLookup || failing.Fallback {
		t.Fatalf("a failing fallback must keep the rule decision: %+v", failing)
	}
}
