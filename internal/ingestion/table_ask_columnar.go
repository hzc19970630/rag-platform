package ingestion

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/tableqa"
)

// sqlColumnNames mirrors tablehouse._sql_columns so generated statements bind
// to the same Parquet columns the catalog exposes.
func sqlColumnNames(header []string) []string {
	names := make([]string, len(header))
	seen := make(map[string]bool, len(header))
	for index, raw := range header {
		name := strings.TrimSpace(raw)
		if name == "" {
			name = fmt.Sprintf("column_%d", index+1)
		}
		candidate := name
		for suffix := 2; seen[strings.ToLower(candidate)] || strings.EqualFold(candidate, "__row_number"); suffix++ {
			candidate = fmt.Sprintf("%s_%d", name, suffix)
		}
		seen[strings.ToLower(candidate)] = true
		names[index] = candidate
	}
	return names
}

func quoteSQL(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (s *Service) columnarTable(asset models.TableAsset) bool {
	return s != nil && s.tables != nil && strings.TrimSpace(asset.StorageURI) != ""
}

func columnarWhere(header []string, filters []TableFilter) (string, []any, error) {
	columns := sqlColumnNames(header)
	index := make(map[string]int, len(header))
	for position, column := range header {
		index[strings.ToLower(strings.TrimSpace(column))] = position
	}
	clauses := make([]string, 0, len(filters))
	params := make([]any, 0, len(filters))
	for _, filter := range filters {
		position, ok := index[strings.ToLower(strings.TrimSpace(filter.Column))]
		if !ok {
			return "", nil, fmt.Errorf("filter column %q is not in the table header", filter.Column)
		}
		column := quoteSQL(columns[position])
		value := strings.TrimSpace(filter.Value)
		switch strings.ToLower(strings.TrimSpace(filter.Op)) {
		case "eq":
			clauses = append(clauses, "CAST("+column+" AS VARCHAR) = ?")
			params = append(params, value)
		case "ne":
			clauses = append(clauses, "CAST("+column+" AS VARCHAR) <> ?")
			params = append(params, value)
		case "contains":
			clauses = append(clauses, "LOWER(CAST("+column+" AS VARCHAR)) LIKE ?")
			params = append(params, "%"+strings.ToLower(value)+"%")
		case "gt", "gte", "lt", "lte":
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return "", nil, fmt.Errorf("numeric filter %q is not a number", value)
			}
			operator := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[strings.ToLower(filter.Op)]
			clauses = append(clauses, "TRY_CAST("+column+" AS DOUBLE) "+operator+" ?")
			params = append(params, number)
		default:
			return "", nil, fmt.Errorf("unsupported filter op %q", filter.Op)
		}
	}
	return strings.Join(clauses, " AND "), params, nil
}

func columnarKeywordClause(header []string, question string) (string, []any, string, []any) {
	tokens := tableqa.Tokens(question)
	if len(tokens) > 8 {
		tokens = tokens[:8]
	}
	if len(tokens) == 0 {
		return "", nil, "", nil
	}
	columns := sqlColumnNames(header)
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quoteSQL(column))
	}
	expression := "LOWER(concat_ws(' ', " + strings.Join(quoted, ", ") + "))"
	whereClauses := make([]string, 0, len(tokens))
	whereParams := make([]any, 0, len(tokens))
	orderParts := make([]string, 0, len(tokens))
	orderParams := make([]any, 0, len(tokens))
	for _, token := range tokens {
		pattern := "%" + token + "%"
		whereClauses = append(whereClauses, expression+" LIKE ?")
		whereParams = append(whereParams, pattern)
		orderParts = append(orderParts, "CASE WHEN "+expression+" LIKE ? THEN 1 ELSE 0 END")
		orderParams = append(orderParams, pattern)
	}
	return "(" + strings.Join(whereClauses, " OR ") + ")", whereParams, "(" + strings.Join(orderParts, " + ") + ") DESC", orderParams
}

func combineWhere(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(kept, " AND ")
}

func (s *Service) columnarCount(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, where string, params []any) (int64, error) {
	result, err := s.QueryTableSQL(ctx, principal, knowledgeBaseID, TableSQLQuery{
		AssetID: asset.ID, SQL: "SELECT COUNT(*) AS value FROM t" + where, Params: params, Limit: 1,
	})
	if err != nil {
		return 0, err
	}
	if len(result.Rows) > 0 && len(result.Rows[0]) > 0 {
		if value, ok := numericCell(result.Rows[0][0]); ok {
			return int64(value), nil
		}
	}
	return 0, nil
}

func (s *Service) executeColumnarSQL(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, sql string, params []any, preview bool, limit int) (TableSQLResult, error) {
	return s.QueryTableSQL(ctx, principal, knowledgeBaseID, TableSQLQuery{
		AssetID: asset.ID, SQL: sql, Params: params, Preview: preview, Limit: limit,
	})
}

func columnarRows(header []string, result TableSQLResult) []TableRowView {
	names := sqlColumnNames(header)
	positions := make(map[string]int, len(result.Columns))
	for index, column := range result.Columns {
		positions[strings.ToLower(column.Name)] = index
	}
	rowNumberIndex, hasRowNumber := positions[strings.ToLower("__row_number")]
	cellsIndex := make([]int, len(header))
	for index, name := range names {
		cellsIndex[index] = -1
		if position, ok := positions[strings.ToLower(name)]; ok {
			cellsIndex[index] = position
		}
	}
	rows := make([]TableRowView, 0, len(result.Rows))
	for _, values := range result.Rows {
		cells := make(map[string]string, len(header))
		for index, column := range header {
			position := cellsIndex[index]
			if position < 0 || position >= len(values) {
				continue
			}
			if values[position] != nil {
				cells[column] = fmt.Sprint(values[position])
			}
		}
		rowNumber := 0
		if hasRowNumber && rowNumberIndex < len(values) {
			if parsed, ok := numericCell(values[rowNumberIndex]); ok {
				rowNumber = int(parsed)
			}
		}
		rows = append(rows, TableRowView{RowNumber: rowNumber, Cells: cells})
	}
	return rows
}

func numericCell(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (s *Service) askTableColumnar(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, header []string, plan tableqa.Classification, input TableAskInput) (TableAskResult, error) {
	if plan.Operation == tableqa.OpRows {
		return s.lookupColumnar(ctx, principal, knowledgeBaseID, asset, header, input, plan)
	}
	columnar := TableAskSQLPlan{
		Operation: plan.Operation, Column: plan.Column, Filters: input.Filters,
		OrderBy: plan.Column, Desc: plan.Desc, Limit: plan.TopN, Confidence: 1,
	}
	return s.runColumnarPlan(ctx, principal, knowledgeBaseID, asset, header, columnar)
}

func (s *Service) lookupColumnar(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, header []string, input TableAskInput, plan tableqa.Classification) (TableAskResult, error) {
	whereFilters, filterParams, err := columnarWhere(header, input.Filters)
	if err != nil {
		return TableAskResult{}, err
	}
	whereKeyword, keywordParams, scoreOrder, scoreParams := columnarKeywordClause(header, input.Question)
	params := append(append([]any{}, filterParams...), keywordParams...)
	where := combineWhere(whereFilters, whereKeyword)
	limit := normalizeLimit(input.Limit, 20, 200)
	order := "__row_number"
	allParams := params
	if scoreOrder != "" {
		order = scoreOrder + ", __row_number"
		allParams = append(append([]any{}, params...), scoreParams...)
	}
	sql := "SELECT * FROM t" + where + " ORDER BY " + order + " LIMIT " + strconv.Itoa(limit)
	result, err := s.executeColumnarSQL(ctx, principal, knowledgeBaseID, asset, sql, allParams, false, limit)
	if err != nil {
		return TableAskResult{}, err
	}
	rows := columnarRows(header, result)
	tokens := tableqa.Tokens(input.Question)
	type scoredRow struct {
		row   TableRowView
		score int
	}
	scored := make([]scoredRow, 0, len(rows))
	for _, row := range rows {
		score := rowKeywordScore(tokens, row.Cells)
		if score > 0 {
			scored = append(scored, scoredRow{row: row, score: score})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].row.RowNumber < scored[j].row.RowNumber
	})
	answer := TableAskResult{
		Route: plan.Route, Operation: tableqa.OpRows, Confidence: plan.Confidence, MatchedRules: plan.MatchedRules,
		AssetID: asset.ID, DocumentID: asset.DocumentID, Sheet: asset.Sheet, Header: header,
		TotalRows: asset.RowCount, SourceRows: asset.TotalRows, IndexMode: asset.IndexMode,
		StorageMode: asset.StorageMode, ParseStatus: asset.ParseStatus, Truncated: asset.Truncated,
		Rows: []TableRowView{},
	}
	count, err := s.columnarCount(ctx, principal, knowledgeBaseID, asset, where, params)
	if err != nil {
		return TableAskResult{}, err
	}
	answer.MatchedRows = int(count)
	for index, item := range scored {
		if index >= limit {
			break
		}
		answer.Rows = append(answer.Rows, item.row)
	}
	if len(answer.Rows) == 0 {
		answer.Notes = append(answer.Notes, "no row matched the question keywords")
	}
	return answer, nil
}

func (s *Service) columnarSampleRows(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, header []string, where string, params []any, limit int) ([]TableRowView, error) {
	if limit <= 0 {
		limit = 10
	}
	sql := "SELECT * FROM t" + where + " ORDER BY __row_number LIMIT " + strconv.Itoa(limit)
	result, err := s.executeColumnarSQL(ctx, principal, knowledgeBaseID, asset, sql, params, false, limit)
	if err != nil {
		return nil, err
	}
	return columnarRows(header, result), nil
}

func (s *Service) runColumnarPlan(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, header []string, plan TableAskSQLPlan) (TableAskResult, error) {
	compiled, err := compileColumnarPlan(header, plan)
	if err != nil {
		return TableAskResult{}, err
	}
	result, err := s.executeColumnarSQL(ctx, principal, knowledgeBaseID, asset, compiled.SQL, compiled.Params, false, compiled.Limit)
	if err != nil {
		return TableAskResult{}, err
	}
	answer := TableAskResult{
		Route: tableqa.RouteAggregate, Operation: plan.Operation, Confidence: plan.Confidence,
		MatchedRules: []string{"table_sql"}, AssetID: asset.ID, DocumentID: asset.DocumentID,
		Sheet: asset.Sheet, Header: header, Column: plan.Column, TopN: plan.Limit, Desc: plan.Desc,
		TotalRows: asset.RowCount, SourceRows: asset.TotalRows, IndexMode: asset.IndexMode,
		StorageMode: asset.StorageMode, ParseStatus: asset.ParseStatus, Truncated: asset.Truncated,
		Rows: []TableRowView{},
	}
	if plan.Operation == tableqa.OpCount {
		if len(result.Rows) > 0 && len(result.Rows[0]) > 0 {
			value, ok := numericCell(result.Rows[0][0])
			if !ok {
				return TableAskResult{}, fmt.Errorf("count result is not numeric")
			}
			answer.Value = &value
			answer.MatchedRows = int(value)
			sample, err := s.columnarSampleRows(ctx, principal, knowledgeBaseID, asset, header, compiled.Where, compiled.Params, 10)
			if err != nil {
				return TableAskResult{}, err
			}
			answer.Rows = sample
		}
		return answer, nil
	}
	if plan.Operation == tableqa.OpSum || plan.Operation == tableqa.OpAvg || plan.Operation == tableqa.OpMin || plan.Operation == tableqa.OpMax {
		if len(result.Rows) == 0 || len(result.Rows[0]) == 0 || result.Rows[0][0] == nil {
			answer.Notes = append(answer.Notes, "no rows matched the generated filters")
			return answer, nil
		}
		value, ok := numericCell(result.Rows[0][0])
		if !ok {
			return TableAskResult{}, fmt.Errorf("aggregate result is not numeric")
		}
		answer.Value = &value
		matched, err := s.columnarCount(ctx, principal, knowledgeBaseID, asset, compiled.Where, compiled.Params)
		if err != nil {
			return TableAskResult{}, err
		}
		answer.MatchedRows = int(matched)
		sample, err := s.columnarSampleRows(ctx, principal, knowledgeBaseID, asset, header, compiled.Where, compiled.Params, 10)
		if err != nil {
			return TableAskResult{}, err
		}
		answer.Rows = sample
		return answer, nil
	}
	answer.Rows = columnarRows(header, result)
	matched, err := s.columnarCount(ctx, principal, knowledgeBaseID, asset, compiled.Where, compiled.Params)
	if err != nil {
		return TableAskResult{}, err
	}
	answer.MatchedRows = int(matched)
	order := "descending"
	if !plan.Desc {
		order = "ascending"
	}
	answer.Notes = append(answer.Notes, "ordered by "+plan.Column+" "+order)
	return answer, nil
}

type compiledColumnarPlan struct {
	SQL    string
	Where  string
	Params []any
	Limit  int
}

func compileColumnarPlan(header []string, plan TableAskSQLPlan) (compiledColumnarPlan, error) {
	whereFilters, filterParams, err := columnarWhere(header, plan.Filters)
	if err != nil {
		return compiledColumnarPlan{}, err
	}
	where := combineWhere(whereFilters)
	columns := sqlColumnNames(header)
	column := ""
	for _, name := range columns {
		if strings.EqualFold(name, plan.Column) {
			column = quoteSQL(name)
			break
		}
	}
	if plan.Operation != tableqa.OpCount && column == "" {
		return compiledColumnarPlan{}, fmt.Errorf("column %q is not in the table header", plan.Column)
	}
	limit := plan.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 500 {
		limit = 500
	}
	var sql string
	switch plan.Operation {
	case tableqa.OpCount:
		sql = "SELECT COUNT(*) AS value FROM t" + where
		limit = 1
	case tableqa.OpSum:
		sql = "SELECT SUM(TRY_CAST(" + column + " AS DOUBLE)) AS value FROM t" + where
		limit = 1
	case tableqa.OpAvg:
		sql = "SELECT AVG(TRY_CAST(" + column + " AS DOUBLE)) AS value FROM t" + where
		limit = 1
	case tableqa.OpMin:
		sql = "SELECT MIN(TRY_CAST(" + column + " AS DOUBLE)) AS value FROM t" + where
		limit = 1
	case tableqa.OpMax:
		sql = "SELECT MAX(TRY_CAST(" + column + " AS DOUBLE)) AS value FROM t" + where
		limit = 1
	case tableqa.OpTop:
		order := "DESC"
		if !plan.Desc {
			order = "ASC"
		}
		orderColumn := column
		if plan.OrderBy != "" && !strings.EqualFold(plan.OrderBy, plan.Column) {
			for _, name := range columns {
				if strings.EqualFold(name, plan.OrderBy) {
					orderColumn = quoteSQL(name)
					break
				}
			}
		}
		sql = "SELECT * FROM t" + where + " ORDER BY TRY_CAST(" + orderColumn + " AS DOUBLE) " + order + " NULLS LAST LIMIT " + strconv.Itoa(limit)
	default:
		return compiledColumnarPlan{}, fmt.Errorf("unsupported operation %q", plan.Operation)
	}
	return compiledColumnarPlan{SQL: sql, Where: where, Params: filterParams, Limit: limit}, nil
}

func (s *Service) askTableSQLColumnar(ctx context.Context, principal authz.Principal, knowledgeBaseID string, asset models.TableAsset, header []string, plan TableAskSQLPlan, preview bool) (TableAskSQLResult, error) {
	compiled, err := compileColumnarPlan(header, plan)
	if err != nil {
		return TableAskSQLResult{}, err
	}
	estimated, err := s.columnarCount(ctx, principal, knowledgeBaseID, asset, compiled.Where, compiled.Params)
	if err != nil {
		return TableAskSQLResult{}, err
	}
	result := TableAskSQLResult{Preview: preview, SQL: compiled.SQL, Plan: &plan, EstimatedRows: estimated}
	if preview {
		if _, err := s.executeColumnarSQL(ctx, principal, knowledgeBaseID, asset, compiled.SQL, compiled.Params, true, compiled.Limit); err != nil {
			return TableAskSQLResult{}, err
		}
		result.Notes = append(result.Notes, "preview only: no rows were returned")
		return result, nil
	}
	answer, err := s.runColumnarPlan(ctx, principal, knowledgeBaseID, asset, header, plan)
	if err != nil {
		return TableAskSQLResult{}, err
	}
	result.Result = &answer
	result.MatchedRows = answer.MatchedRows
	result.Notes = append(result.Notes, "executed against the columnar table copy")
	return result, nil
}
