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
	"github.com/hzc19970630/rag-platform/internal/tableqa"
)

const (
	tableAskSQLDefaultMaxRows       = 200
	tableAskSQLDefaultMinConfidence = 0.7
	tableAskSQLDefaultTimeout       = 20 * time.Second
	tableAskSQLMaxFilters           = 5
)

// TableAskSQLPlan is the only shape the model may return. The server compiles it
// into a read-only SELECT over the B39 columnar copy; the model never supplies
// SQL text.
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

// TableAskSQLResult reports the compiled SQL. Preview validates the statement
// and counts matching rows; execute fills Result with the answer and citations.
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

// TableAskSQLOptions wires the optional SQL planner. A nil Chat keeps the
// endpoint on the deterministic fallback path instead of failing the request.
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
		options.MaxRows = tableAskSQLDefaultMaxRows
	}
	if options.MinConfidence <= 0 {
		options.MinConfidence = tableAskSQLDefaultMinConfidence
	}
	if options.Timeout <= 0 {
		options.Timeout = tableAskSQLDefaultTimeout
	}
	s.tableSQL = options
}

// AskTableSQL turns an aggregate/TopN question into a constrained plan, compiles
// it to a read-only SELECT over the columnar copy, and previews or executes it.
// A rejected plan falls back to the deterministic AskTable path.
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
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "route="+routing.Route+" is not aggregate/TopN")
	}
	if asset.MaterializeStatus == MaterializeStatusFailed {
		message := asset.MaterializeError
		if message == "" {
			message = "materialization failed"
		}
		return TableAskSQLResult{}, apperror.Invalidf("table_materialize_failed: sheet %s: %s", asset.Sheet, message)
	}
	if strings.TrimSpace(asset.StorageURI) == "" {
		return TableAskSQLResult{}, apperror.Invalidf("table_needs_reparse: sheet %s has no columnar copy; reparse the document", asset.Sheet)
	}
	if s.tableSQL.Chat == nil || s.tableSQL.RenderPrompt == nil {
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "sql_planner_unavailable")
	}
	timeout := s.tableSQL.Timeout
	planCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	modelName := ""
	if s.tableSQL.Models != nil {
		resolved, err := s.tableSQL.Models.ChatModel(planCtx, principal.TenantID, knowledgeBaseID)
		if err != nil {
			return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "chat_model_unavailable: "+err.Error())
		}
		modelName = resolved
	}
	systemPrompt, err := s.tableSQL.RenderPrompt(tableAssetSummary(asset), question)
	if err != nil {
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "prompt_render_failed")
	}
	response, err := s.tableSQL.Chat.Complete(planCtx, model.ChatRequest{
		SystemInstruction: systemPrompt, Query: question, MaxTokens: 512, Model: modelName,
	})
	if err != nil {
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "model_error")
	}
	plan, err := parseTableAskSQLPlan(response.Text, header, s.tableSQL.MaxRows)
	if err != nil {
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "plan_invalid: "+err.Error())
	}
	if plan.Confidence < s.tableSQL.MinConfidence {
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "low_confidence")
	}
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := s.askTableSQLColumnar(ctx, principal, knowledgeBaseID, asset, header, plan, input.Preview)
	if err != nil {
		return s.tableAskSQLFallback(ctx, principal, knowledgeBaseID, input, asset, "columnar_sql_failed: "+err.Error())
	}
	return result, nil
}

func (s *Service) tableAskSQLFallback(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input TableAskSQLInput, asset models.TableAsset, reason string) (TableAskSQLResult, error) {
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
	if err := s.recordWorkspaceAudit(ctx, principal, "table_query_fallback", "table_asset", asset.ID, reason); err != nil {
		return TableAskSQLResult{}, err
	}
	return result, nil
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
	if len(plan.Filters) > tableAskSQLMaxFilters {
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
