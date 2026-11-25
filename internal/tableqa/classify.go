// Package tableqa routes a natural-language table question to a deterministic
// execution strategy. Rules run first and always produce a route; a model
// classifier is only consulted by callers that explicitly supply a fallback.
package tableqa

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	RouteLookup    = "lookup"
	RouteAggregate = "aggregate"
	RouteExplain   = "explain"
)

const (
	OpRows  = "rows"
	OpCount = "count"
	OpSum   = "sum"
	OpAvg   = "avg"
	OpMin   = "min"
	OpMax   = "max"
	OpTop   = "top"
)

var (
	schemaTable     = regexp.MustCompile(`(?i)(哪些|哪几个|几个|多少)(个)?(列|字段|表头)|列名|字段名|表头|表结构|字段类型|数据结构|schema|\bcolumns?\b|\bfields?\b`)
	meaningTable    = regexp.MustCompile(`(?i)是什么|什么意思|介绍|说明一下|解释|含义|作用|用途|干什么用|用来做什么|what is|what's|explain|describe|meaning of`)
	countTable      = regexp.MustCompile(`(?i)多少行|几行|行数|多少条|条数|记录数|条记录|数量是多少|数量有多少|多少个|计数|\bcount\b|\bnumber of (rows|records)\b`)
	sumTable        = regexp.MustCompile(`(?i)总和|合计|求和|总计|总金额|总额|加总|总量|\bsum\b|\btotal\b`)
	avgTable        = regexp.MustCompile(`(?i)平均|均值|\baverage\b|\bavg\b|\bmean\b`)
	maxTable        = regexp.MustCompile(`(?i)最高|最大|最多|峰值|\bmax(imum)?\b|\bhighest\b|\blargest\b`)
	minTable        = regexp.MustCompile(`(?i)最低|最小|最少|\bmin(imum)?\b|\blowest\b|\bsmallest\b`)
	topTable        = regexp.MustCompile(`(?i)(?:前|top\s*|头)\s*([0-9]+|[一二三四五六七八九十百两]+)\s*(?:名|个|条|行|位)?|排行|排名|降序|升序|top\s*rows|\branked\b`)
	extremeTopTable = regexp.MustCompile(`(?i)(?:最高|最大|最多|最低|最小|最少)的?\s*([0-9]+|[一二三四五六七八九十百两]+)\s*(?:名|个|条|行|位)`)
	ascendingTable  = regexp.MustCompile(`(?i)最低|最小|最少|升序|从小到|ascending|\basc\b`)
	lookupTable     = regexp.MustCompile(`(?i)哪(个|些|一|张|条|行|位)|谁|何时|什么时候|哪里|在哪|找出|查找|查询|列出|显示|包含|有没有|是否|是多少|叫什么|which|who|when|where|list|show|find|lookup`)
)

// Classification is the routing decision for one question. MatchedRules is
// recorded so an answer can explain why a route was chosen.
type Classification struct {
	Route        string   `json:"route"`
	Operation    string   `json:"operation,omitempty"`
	Confidence   float64  `json:"confidence"`
	MatchedRules []string `json:"matchedRules,omitempty"`
	TopN         int      `json:"topN,omitempty"`
	Desc         bool     `json:"desc,omitempty"`
	Column       string   `json:"column,omitempty"`
	NeedsColumn  bool     `json:"needsColumn,omitempty"`
	Fallback     bool     `json:"fallback,omitempty"`
}

// Fallback classifies a question the rule engine could not place confidently.
// Implementations must be safe to call on user text and should return a
// Classification with Fallback=true.
type Fallback func(ctx context.Context, question string) (Classification, error)

// Classify applies the rule table. Unknown questions default to lookup with a
// low confidence rather than failing the request.
func Classify(question string) Classification {
	text := strings.ToLower(strings.TrimSpace(question))
	if text == "" {
		return Classification{Route: RouteLookup, Operation: OpRows, MatchedRules: []string{"empty"}}
	}
	if schemaTable.MatchString(text) {
		return withExtraRules(Classification{Route: RouteExplain, Confidence: 0.95, MatchedRules: []string{"explain.schema"}}, text, "explain.schema")
	}
	if meaningTable.MatchString(text) {
		return withExtraRules(Classification{Route: RouteExplain, Confidence: 0.85, MatchedRules: []string{"explain.meaning"}}, text, "explain.meaning")
	}
	if match := topTable.FindStringSubmatch(text); len(match) > 0 {
		return topClassification(text, match)
	}
	if match := extremeTopTable.FindStringSubmatch(text); len(match) > 0 {
		return topClassification(text, match)
	}
	if op, rule, at := firstOperation(text); at >= 0 {
		confidence := 0.88
		if op == OpCount {
			confidence = 0.82
		}
		return withExtraRules(Classification{Route: RouteAggregate, Operation: op, Confidence: confidence, MatchedRules: []string{rule}}, text, rule)
	}
	if lookupTable.MatchString(text) {
		return withExtraRules(Classification{Route: RouteLookup, Operation: OpRows, Confidence: 0.75, MatchedRules: []string{"lookup"}}, text, "lookup")
	}
	return withExtraRules(Classification{Route: RouteLookup, Operation: OpRows, Confidence: 0.4, MatchedRules: []string{"default.lookup"}}, text, "default.lookup")
}

func topClassification(text string, match []string) Classification {
	result := Classification{
		Route: RouteAggregate, Operation: OpTop, Confidence: 0.9,
		MatchedRules: []string{"aggregate.top"}, Desc: true, TopN: topN(match),
	}
	if ascendingTable.MatchString(text) {
		result.Desc = false
		result.MatchedRules = append(result.MatchedRules, "aggregate.top.asc")
	}
	return withExtraRules(result, text, "aggregate.top")
}

// ClassifyWithFallback consults fallback only when the rule confidence is below
// the threshold. A failing fallback leaves the rule decision in place.
func ClassifyWithFallback(ctx context.Context, question string, fallback Fallback) Classification {
	result := Classify(question)
	if fallback == nil || result.Confidence >= 0.5 {
		return result
	}
	fallbackResult, err := fallback(ctx, question)
	if err != nil || fallbackResult.Route == "" {
		return result
	}
	fallbackResult.Fallback = true
	fallbackResult.MatchedRules = append(fallbackResult.MatchedRules, "model.fallback")
	return fallbackResult
}

// Resolve classifies the question and resolves the target column and TopN
// against the table header. NeedsColumn reports an aggregate the rule engine
// recognized but could not bind to a column.
func Resolve(question string, header []string) Classification {
	result := Classify(question)
	if result.Route != RouteAggregate {
		return result
	}
	if columns := MatchColumns(question, header); len(columns) > 0 {
		result.Column = columns[0]
	}
	switch result.Operation {
	case OpSum, OpAvg, OpMin, OpMax, OpTop:
		result.NeedsColumn = result.Column == ""
	}
	return result
}

// MatchColumns lists header columns named by the question, longest alias first.
// It is used both for column binding and for selecting among several tables.
func MatchColumns(question string, header []string) []string {
	text := strings.ToLower(question)
	type match struct {
		column string
		runes  int
	}
	matches := make([]match, 0, len(header))
	for _, column := range header {
		longest := 0
		for _, alias := range aliases(column) {
			if !meaningfulAlias(alias) || !strings.Contains(text, alias) {
				continue
			}
			if size := len([]rune(alias)); size > longest {
				longest = size
			}
		}
		if longest > 0 {
			matches = append(matches, match{column: column, runes: longest})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].runes > matches[j].runes })
	columns := make([]string, 0, len(matches))
	for _, item := range matches {
		columns = append(columns, item.column)
	}
	return columns
}

func aliases(column string) []string {
	base := strings.ToLower(strings.TrimSpace(column))
	result := []string{base}
	for _, separator := range []string{"（", "(", "：", ":", "/", "\\"} {
		if index := strings.Index(base, separator); index > 0 {
			result = append(result, strings.TrimSpace(base[:index]))
		}
	}
	for _, separator := range []string{"_", "-", " ", "　"} {
		if strings.Contains(base, separator) {
			for _, part := range strings.FieldsFunc(base, func(r rune) bool { return string(r) == separator }) {
				result = append(result, strings.TrimSpace(part))
			}
		}
	}
	return result
}

func meaningfulAlias(alias string) bool {
	runes := []rune(alias)
	if len(runes) < 2 {
		return false
	}
	if containsHan(runes) {
		return true
	}
	if len(runes) < 3 {
		return false
	}
	switch alias {
	case "the", "and", "for", "with", "from", "into", "that":
		return false
	}
	return true
}

func containsHan(runes []rune) bool {
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func withExtraRules(result Classification, text, exclude string) Classification {
	for _, extra := range []struct {
		rule string
		re   *regexp.Regexp
	}{
		{"lookup", lookupTable},
		{"aggregate.count", countTable},
	} {
		if extra.rule == exclude || !extra.re.MatchString(text) {
			continue
		}
		result.MatchedRules = append(result.MatchedRules, extra.rule)
		if result.Confidence < 0.99 {
			result.Confidence += 0.02
		}
	}
	if result.Confidence > 0.99 {
		result.Confidence = 0.99
	}
	return result
}

func firstOperation(text string) (string, string, int) {
	type candidate struct {
		op   string
		rule string
		at   int
	}
	candidates := []candidate{
		{OpCount, "aggregate.count", firstIndex(text, countTable)},
		{OpSum, "aggregate.sum", firstIndex(text, sumTable)},
		{OpAvg, "aggregate.avg", firstIndex(text, avgTable)},
		{OpMax, "aggregate.max", firstIndex(text, maxTable)},
		{OpMin, "aggregate.min", firstIndex(text, minTable)},
	}
	selected := candidate{at: -1}
	for _, item := range candidates {
		if item.at < 0 {
			continue
		}
		if selected.at < 0 || item.at < selected.at {
			selected = item
		}
	}
	return selected.op, selected.rule, selected.at
}

func firstIndex(text string, table *regexp.Regexp) int {
	location := table.FindStringIndex(text)
	if location == nil {
		return -1
	}
	return location[0]
}

// Tokens extracts lookup terms from a question. Han runs are split on common
// particles and expanded into 2-4 rune grams; ASCII words are kept whole.
func Tokens(question string) []string {
	text := strings.ToLower(strings.TrimSpace(question))
	seen := map[string]bool{}
	tokens := make([]string, 0, 16)
	add := func(token string) {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] || stopToken(token) {
			return
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	var han []rune
	var word []rune
	flushHan := func() {
		if len(han) >= 2 {
			for size := 2; size <= 4; size++ {
				for start := 0; start+size <= len(han); start++ {
					add(string(han[start : start+size]))
				}
			}
		}
		han = han[:0]
	}
	flushWord := func() {
		if len(word) >= 2 {
			add(string(word))
		}
		word = word[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			if strings.ContainsRune("的了吗呢吧啊请帮我你他她它这那个些", r) {
				flushHan()
				continue
			}
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushHan()
			flushWord()
		}
	}
	flushHan()
	flushWord()
	sort.Strings(tokens)
	return tokens
}

func stopToken(token string) bool {
	switch token {
	case "多少", "哪些", "哪个", "什么", "怎么", "如何", "是否", "请问", "一下",
		"数据", "表格", "表里", "文件", "信息", "里面", "这张", "那个", "这些", "那些",
		"查询", "查找", "列出", "显示", "找出", "the", "and", "for", "with":
		return true
	}
	return false
}

// ParseNumber parses a table cell into a number, tolerating thousands
// separators, currency symbols and a trailing percent sign.
func ParseNumber(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" || text == "-" || text == "—" || text == "n/a" || text == "N/A" {
		return 0, false
	}
	negative := false
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
		negative = true
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	text = strings.TrimSuffix(text, "%")
	replacer := strings.NewReplacer(",", "", "，", "", " ", "", "￥", "", "¥", "", "$", "", "元", "")
	text = replacer.Replace(text)
	if text == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	if negative {
		value = -value
	}
	return value, true
}

func topN(match []string) int {
	if len(match) < 2 {
		return 10
	}
	raw := strings.TrimSpace(match[1])
	if raw == "" {
		return 10
	}
	if value, err := strconv.Atoi(raw); err == nil {
		if value <= 0 {
			return 10
		}
		if value > 100 {
			return 100
		}
		return value
	}
	if value := chineseNumber(raw); value > 0 {
		if value > 100 {
			return 100
		}
		return value
	}
	return 10
}

func chineseNumber(raw string) int {
	digits := map[rune]int{'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, current := 0, 0
	for _, r := range raw {
		switch r {
		case '十':
			if current == 0 {
				current = 1
			}
			total += current * 10
			current = 0
		case '百':
			if current == 0 {
				current = 1
			}
			total += current * 100
			current = 0
		default:
			digit, ok := digits[r]
			if !ok {
				return 0
			}
			current = digit
		}
	}
	return total + current
}
