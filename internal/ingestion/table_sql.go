package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/tableqa"
	"gorm.io/gorm"
)

const (
	tableSQLDefaultMaxRows       = 200
	tableSQLDefaultMinConfidence = 0.7
	tableSQLDefaultTimeout       = 20 * time.Second
	tableSQLMaxFilters           = 5
)

// TableAskSQLPlan is the only shape the model may return. The server compiles it
// into SELECT statements over table_rows; the model never supplies SQL text.
type TableAskSQLPlan struct {
	Operation  string        `json:"operation"`
	Column     string        `json:"column,omitempty"`
	Filters    []TableFilter `json:"filters,omitempty"`
	OrderBy    string        `json:"orderBy,omitempty"`
	Desc       bool          `json:"desc,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Confidence float64       `json:"confidence"`
}

// TableAskSQLInput is one natural-language aggregate/TopN question.
type TableAskSQLInput struct {
	Question string
	AssetID  string
	Preview  bool
}

// TableAskSQLResult always reports the compiled SQL. Preview stops before reading
// rows; execute fills Result with the deterministic answer and row evidence.
// Fallback means the model plan was rejected and AskTable answered instead.
type TableAskSQLResult struct {
	Preview        bool             `json:"preview"`
	SQL            string           `json:"sql"`
	Plan           *TableAskSQLPlan `json:"plan,omitempty"`
	EstimatedRows  int64            `json:"estimatedRows"`
	Fallback       bool             `json:"fallback"`
	FallbackReason string           `json:"fallbackReason,omitempty"`
	Result         *TableAskResult  `json:"result,omitempty"`
	MatchedRows    int              `json:"matchedRows"`
	ElapsedMs      int64            `json:"elapsedMs"`
	Notes          []string         `json:"notes,omitempty"`
}

// TableChatModelResolver resolves the knowledge-base chat binding. The registry
// applies provider health, fallback and quota before returning a model name.
type TableChatModelResolver interface {
	ChatModel(ctx context.Context, tenantID, knowledgeBaseID string) (string, error)
}

// TableAskSQLOptions wires the optional SQL planner. A nil Chat keeps the endpoint
// on the deterministic fallback path instead of failing the request.
type TableAskSQLOptions struct {
	Chat          model.Chatter
	Models        TableChatModelResolver
	RenderPrompt  func(schema, question string) (string, error)
	MinConfidence float64
	MaxRows       int
	Timeout       time.Duration
}

func (s *Service) SetTableAskSQLOptions(options TableAskSQLOptions) {
	if options.MaxRows <= 0 {
		options.MaxRows = tableSQLDefaultMaxRows
	}
	if options.MinConfidence <= 0 {
		options.MinConfidence = tableSQLDefaultMinConfidence
	}
	if options.Timeout <= 0 {
		options.Timeout = tableSQLDefaultTimeout
	}
	s.tableSQL = options
}

// AskTableSQL generates a constrained plan, compiles it to a read-only SELECT
// and either previews it or executes it. Any planner/validation/limit failure
// falls back to AskTable and records the reason in the audit trail.
func (s *Service) AskTableSQL(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input TableAskSQLInput) (TableAskSQLResult, error) {
	started := time.Now()
	result, err := s.askTableSQL(ctx, principal, knowledgeBaseID, input)
	if s.metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		s.metrics.Inc("ingestion_table_sql_total", map[string]string{
			"preview": strconv.FormatBool(input.Preview), "fallback": strconv.FormatBool(result.Fallback), "status": status,
		})
		s.metrics.Observe("ingestion_table_sql_duration", time.Since(started), map[string]string{"preview": strconv.FormatBool(input.Preview)})
	}
	if err == nil {
		result.ElapsedMs = time.Since(started).Milliseconds()
	}
	return result, err
}

func (s *Service) askTableSQL(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input TableAskSQLInput) (TableAskSQLResult, error) {
	question := strings.TrimSpace(input.Question)
	if question == "" {
		return TableAskSQLResult{}, apperror.Invalidf("table_question_required: question must not be empty")
	}
	if len([]rune(question)) > 1000 {
		return TableAskSQLResult{}, apperror.Invalidf("table_question_too_long: question must be at most 1000 characters")
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return TableAskSQLResult{}, err
	}
	asset, err := s.selectTableAsset(ctx, principal, knowledgeBaseID, question, strings.TrimSpace(input.AssetID))
	if err != nil {
		return TableAskSQLResult{}, err
	}
	header := decodeHeader(asset.Header)
	if routing := tableqa.Classify(question); routing.Route != tableqa.RouteAggregate {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "route="+routing.Route+" is not aggregate/TopN")
	}
	if s.tableSQL.Chat == nil || s.tableSQL.RenderPrompt == nil {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "sql_planner_unavailable")
	}
	timeout := s.tableSQL.Timeout
	planCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	modelName := ""
	if s.tableSQL.Models != nil {
		resolved, err := s.tableSQL.Models.ChatModel(planCtx, principal.TenantID, knowledgeBaseID)
		if err != nil {
			return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "chat_model_unavailable: "+err.Error())
		}
		modelName = resolved
	}
	systemPrompt, err := s.tableSQL.RenderPrompt(tableAssetSummary(asset), question)
	if err != nil {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "prompt_render_failed")
	}
	response, err := s.tableSQL.Chat.Complete(planCtx, model.ChatRequest{
		SystemInstruction: systemPrompt, Query: question, MaxTokens: 512, Model: modelName,
	})
	if err != nil {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "model_error")
	}
	plan, err := parseTableAskSQLPlan(response.Text, header, s.tableSQL.MaxRows)
	if err != nil {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "plan_invalid: "+err.Error())
	}
	if plan.Confidence < s.tableSQL.MinConfidence {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "low_confidence")
	}
	if s.columnarTable(asset) {
		result, err := s.askTableSQLColumnar(ctx, principal, knowledgeBaseID, asset, header, plan, input.Preview)
		if err == nil {
			return result, nil
		}
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "columnar_sql_failed: "+err.Error())
	}

	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	result := TableAskSQLResult{Preview: input.Preview, Plan: &plan}
	var (
		count   int64
		sqlText string
		rows    []decodedTableRow
	)
	loadRows := !input.Preview
	err = s.withTableSQLTx(ctx, principal.TenantID, timeout, func(tx *gorm.DB) error {
		if err := tableSQLQuery(tx, principal.TenantID, asset.ID, plan.Filters).Count(&count).Error; err != nil {
			return err
		}
		var sample []models.TableRow
		previewLimit := s.tableSQL.MaxRows
		if plan.Operation == tableqa.OpCount {
			previewLimit = 10
		}
		sqlText = tx.ToSQL(func(tx *gorm.DB) *gorm.DB {
			return tableSQLSelect(tx, principal.TenantID, asset.ID, plan.Filters, previewLimit).Find(&sample)
		})
		if !loadRows || (plan.Operation != tableqa.OpCount && count > int64(s.tableSQL.MaxRows)) {
			return nil
		}
		limit := s.tableSQL.MaxRows
		if plan.Operation == tableqa.OpCount {
			limit = 10
		}
		var stored []models.TableRow
		if err := tableSQLSelect(tx, principal.TenantID, asset.ID, plan.Filters, limit).Find(&stored).Error; err != nil {
			return err
		}
		rows, err = decodeTableRows(stored)
		return err
	})
	if err != nil {
		return TableAskSQLResult{}, err
	}
	result.SQL = sqlText
	result.EstimatedRows = count
	if input.Preview {
		result.Notes = append(result.Notes, "preview only: no rows were read; estimatedRows is COUNT(*) over the same filters")
		if count > int64(s.tableSQL.MaxRows) && plan.Operation != tableqa.OpCount {
			result.Notes = append(result.Notes, fmt.Sprintf("execution would fall back to deterministic filtering because %d rows exceed the %d-row SQL limit", count, s.tableSQL.MaxRows))
		}
		if err := s.auditTableSQL(ctx, principal, asset.ID, "table_query_preview", question, result, 0, nil); err != nil {
			return TableAskSQLResult{}, err
		}
		return result, nil
	}
	if count > int64(s.tableSQL.MaxRows) && plan.Operation != tableqa.OpCount {
		return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset,
			fmt.Sprintf("row_limit_exceeded: %d rows exceed %d", count, s.tableSQL.MaxRows))
	}
	rows = filterTableRows(rows, plan.Filters)
	answer := TableAskResult{
		Route: tableqa.RouteAggregate, Operation: plan.Operation, Confidence: plan.Confidence,
		MatchedRules: []string{"table_sql"}, AssetID: asset.ID, DocumentID: asset.DocumentID,
		Sheet: asset.Sheet, Header: header, Column: plan.Column, TopN: plan.Limit, Desc: plan.Desc,
		TotalRows: asset.RowCount, SourceRows: asset.TotalRows, IndexMode: asset.IndexMode,
		StorageMode: asset.StorageMode, ParseStatus: asset.ParseStatus, Truncated: asset.Truncated,
		Rows: []TableRowView{},
	}
	switch {
	case plan.Operation == tableqa.OpCount:
		value := float64(count)
		answer.Value = &value
		answer.MatchedRows = int(count)
		limit := 10
		for index, row := range rows {
			if index >= limit {
				break
			}
			answer.Rows = append(answer.Rows, TableRowView{RowNumber: row.number, Cells: row.cells})
		}
		answer.Notes = append(answer.Notes, fmt.Sprintf("counted %d rows", count))
	case count == 0:
		answer.Notes = append(answer.Notes, "no rows matched the generated filters")
	case plan.Operation == tableqa.OpTop:
		answer, err = topRows(rows, answer, plan.Limit)
		if err != nil {
			return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, err.Error())
		}
	default:
		answer, err = aggregateRows(rows, answer, s.tableSQL.MaxRows)
		if err != nil {
			return s.tableSQLFallback(ctx, principal, knowledgeBaseID, input, asset, err.Error())
		}
	}
	if plan.Operation == tableqa.OpTop && answer.TopN != 0 {
		answer.TopN = min(plan.Limit, s.tableSQL.MaxRows)
	}
	result.Result = &answer
	result.MatchedRows = answer.MatchedRows
	result.Notes = append(result.Notes, fmt.Sprintf("compiled plan executed over %d matched rows", count))
	if err := s.auditTableSQL(ctx, principal, asset.ID, "table_query_execute", question, result, count, answer.Value); err != nil {
		return TableAskSQLResult{}, err
	}
	return result, nil
}

func (s *Service) tableSQLFallback(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input TableAskSQLInput, asset models.TableAsset, reason string) (TableAskSQLResult, error) {
	result := TableAskSQLResult{
		Preview: input.Preview, Fallback: true, FallbackReason: reason,
		Notes: []string{"fallback to deterministic table filtering: " + reason},
	}
	if !input.Preview {
		answer, err := s.askTable(ctx, principal, knowledgeBaseID, TableAskInput{Question: input.Question, AssetID: input.AssetID})
		if err != nil {
			return TableAskSQLResult{}, err
		}
		result.Result = &answer
	}
	if err := s.auditTableSQL(ctx, principal, asset.ID, "table_query_fallback", input.Question, result, 0, nil); err != nil {
		return TableAskSQLResult{}, err
	}
	return result, nil
}

func (s *Service) withTableSQLTx(ctx context.Context, tenantID string, timeout time.Duration, fn func(tx *gorm.DB) error) error {
	return tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			// Only SELECT statements are ever built by this file. The statement
			// timeout bounds a pathological filter without changing tx semantics.
			millis := int(timeout / time.Millisecond)
			if millis <= 0 {
				millis = 1000
			}
			if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", millis)).Error; err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func tableSQLQuery(tx *gorm.DB, tenantID, assetID string, filters []TableFilter) *gorm.DB {
	query := tenantdb.MustScoped(tx, tenantID).Model(&models.TableRow{}).Where("table_asset_id = ?", assetID)
	for _, filter := range filters {
		expr, args := tableJSONText(tx.Dialector.Name(), filter.Column)
		switch strings.ToLower(strings.TrimSpace(filter.Op)) {
		case "eq":
			query = query.Where(expr+" = ?", append(args, filter.Value)...)
		case "ne":
			query = query.Where(expr+" <> ?", append(args, filter.Value)...)
		case "contains":
			query = query.Where("LOWER("+expr+") LIKE ? ESCAPE '\\'", append(args, "%"+escapeLike(strings.ToLower(filter.Value))+"%")...)
		}
	}
	return query
}

func tableSQLSelect(tx *gorm.DB, tenantID, assetID string, filters []TableFilter, limit int) *gorm.DB {
	return tableSQLQuery(tx, tenantID, assetID, filters).
		Select("row_number, cells").Order("row_number").
		Limit(limit)
}

func tableJSONText(dialect, column string) (string, []any) {
	if dialect == "postgres" {
		return "cells ->> ?", []any{column}
	}
	return "json_extract(cells, ?)", []any{"$." + column}
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func decodeTableRows(stored []models.TableRow) ([]decodedTableRow, error) {
	rows := make([]decodedTableRow, 0, len(stored))
	for _, row := range stored {
		cells := map[string]string{}
		if err := json.Unmarshal([]byte(row.Cells), &cells); err != nil {
			continue
		}
		rows = append(rows, decodedTableRow{number: row.RowNumber, cells: cells})
	}
	return rows, nil
}

func parseTableAskSQLPlan(text string, header []string, maxRows int) (TableAskSQLPlan, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return TableAskSQLPlan{}, fmt.Errorf("model did not return a JSON object")
	}
	var plan TableAskSQLPlan
	if err := json.Unmarshal([]byte(text[start:end+1]), &plan); err != nil {
		return TableAskSQLPlan{}, fmt.Errorf("decode plan: %w", err)
	}
	plan.Operation = strings.ToLower(strings.TrimSpace(plan.Operation))
	switch plan.Operation {
	case tableqa.OpCount, tableqa.OpSum, tableqa.OpAvg, tableqa.OpMin, tableqa.OpMax, tableqa.OpTop:
	default:
		return TableAskSQLPlan{}, fmt.Errorf("unsupported operation %q", plan.Operation)
	}
	if column, ok := whitelistedColumn(columnByLower(header), plan.Column); ok {
		plan.Column = column
	} else if plan.Column != "" {
		return TableAskSQLPlan{}, fmt.Errorf("column %q is not in the table header", plan.Column)
	}
	if plan.Operation != tableqa.OpCount && plan.Column == "" {
		return TableAskSQLPlan{}, fmt.Errorf("operation %s requires a column", plan.Operation)
	}
	if len(plan.Filters) > tableSQLMaxFilters {
		return TableAskSQLPlan{}, fmt.Errorf("too many filters: %d", len(plan.Filters))
	}
	for index := range plan.Filters {
		column, ok := whitelistedColumn(columnByLower(header), plan.Filters[index].Column)
		if !ok {
			return TableAskSQLPlan{}, fmt.Errorf("filter column %q is not in the table header", plan.Filters[index].Column)
		}
		plan.Filters[index].Column = column
		plan.Filters[index].Op = strings.ToLower(strings.TrimSpace(plan.Filters[index].Op))
		switch plan.Filters[index].Op {
		case "eq", "ne", "contains":
		default:
			return TableAskSQLPlan{}, fmt.Errorf("unsupported filter op %q", plan.Filters[index].Op)
		}
		if strings.TrimSpace(plan.Filters[index].Value) == "" {
			return TableAskSQLPlan{}, fmt.Errorf("filter %q has an empty value", column)
		}
	}
	if plan.Operation == tableqa.OpTop {
		if plan.OrderBy == "" {
			plan.OrderBy = plan.Column
		}
		column, ok := whitelistedColumn(columnByLower(header), plan.OrderBy)
		if !ok {
			return TableAskSQLPlan{}, fmt.Errorf("order column %q is not in the table header", plan.OrderBy)
		}
		plan.OrderBy = column
		if plan.Limit <= 0 {
			plan.Limit = 10
		}
		if plan.Limit > maxRows {
			plan.Limit = maxRows
		}
	}
	if plan.Confidence < 0 {
		plan.Confidence = 0
	}
	if plan.Confidence > 1 {
		plan.Confidence = 1
	}
	return plan, nil
}

func columnByLower(header []string) map[string]string {
	columns := make(map[string]string, len(header))
	for _, column := range header {
		columns[strings.ToLower(strings.TrimSpace(column))] = column
	}
	return columns
}

func whitelistedColumn(columns map[string]string, requested string) (string, bool) {
	column, ok := columns[strings.ToLower(strings.TrimSpace(requested))]
	return column, ok
}

func (s *Service) auditTableSQL(ctx context.Context, principal authz.Principal, assetID, action, question string, result TableAskSQLResult, matchedRows int64, value *float64) error {
	detail := map[string]any{
		"question":       question,
		"sql":            result.SQL,
		"plan":           result.Plan,
		"estimatedRows":  result.EstimatedRows,
		"matchedRows":    matchedRows,
		"fallback":       result.Fallback,
		"fallbackReason": result.FallbackReason,
		"preview":        result.Preview,
	}
	if value != nil {
		detail["value"] = *value
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode table query audit: %w", err)
	}
	return s.recordWorkspaceAudit(ctx, principal, action, "table_asset", assetID, string(encoded))
}
