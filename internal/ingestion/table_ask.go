package ingestion

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/tableqa"
)

// TableAskInput is a natural-language table question. Filters and Limit are
// optional deterministic constraints; question routing never depends on them.
type TableAskInput struct {
	Question string
	AssetID  string
	Filters  []TableFilter
	Limit    int
}

// TableAskResult reports the route and the deterministic answer. Value is set
// for counted and numeric aggregates; Rows carries row-level evidence for
// citations. Notes state coverage and ordering caveats in user-facing text.
type TableAskResult struct {
	Route        string         `json:"route"`
	Operation    string         `json:"operation"`
	Confidence   float64        `json:"confidence"`
	MatchedRules []string       `json:"matchedRules"`
	AssetID      string         `json:"assetId"`
	DocumentID   string         `json:"documentId"`
	Sheet        string         `json:"sheet"`
	Header       []string       `json:"header"`
	Column       string         `json:"column,omitempty"`
	TopN         int            `json:"topN,omitempty"`
	Desc         bool           `json:"desc,omitempty"`
	Value        *float64       `json:"value,omitempty"`
	Rows         []TableRowView `json:"rows"`
	Sample       []string       `json:"sample,omitempty"`
	MatchedRows  int            `json:"matchedRows"`
	TotalRows    int            `json:"totalRows"`
	SourceRows   int64          `json:"sourceRows,omitempty"`
	IndexMode    string         `json:"indexMode,omitempty"`
	StorageMode  string         `json:"storageMode,omitempty"`
	ParseStatus  string         `json:"parseStatus,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`
	NeedsColumn  bool           `json:"needsColumn,omitempty"`
	Notes        []string       `json:"notes,omitempty"`
}

// AskTable classifies a table question and executes the deterministic route:
// lookup scans rows for keyword matches, aggregate counts or computes over one
// column, and explain returns table metadata without scanning every row. The
// natural-language Text-to-SQL route is a later B37 step and is not used here.
func (s *Service) AskTable(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input TableAskInput) (TableAskResult, error) {
	started := time.Now()
	result, err := s.askTable(ctx, principal, knowledgeBaseID, input)
	if s.metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		route := result.Route
		if route == "" {
			route = "unknown"
		}
		s.metrics.Inc("ingestion_table_ask_total", map[string]string{"route": route, "status": status})
		s.metrics.Observe("ingestion_table_ask_duration", time.Since(started), map[string]string{"route": route})
	}
	return result, err
}

func (s *Service) askTable(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input TableAskInput) (TableAskResult, error) {
	question := strings.TrimSpace(input.Question)
	if question == "" {
		return TableAskResult{}, apperror.Invalidf("table_question_required: question must not be empty")
	}
	if len([]rune(question)) > 1000 {
		return TableAskResult{}, apperror.Invalidf("table_question_too_long: question must be at most 1000 characters")
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return TableAskResult{}, err
	}
	asset, err := s.selectTableAsset(ctx, principal, knowledgeBaseID, question, strings.TrimSpace(input.AssetID))
	if err != nil {
		return TableAskResult{}, err
	}
	header := decodeHeader(asset.Header)
	plan := tableqa.Resolve(question, header)
	result := TableAskResult{
		Route: plan.Route, Operation: plan.Operation, Confidence: plan.Confidence,
		MatchedRules: plan.MatchedRules, AssetID: asset.ID, DocumentID: asset.DocumentID,
		Sheet: asset.Sheet, Header: header, Column: plan.Column, TopN: plan.TopN, Desc: plan.Desc,
		TotalRows: asset.RowCount, SourceRows: asset.TotalRows, IndexMode: asset.IndexMode,
		StorageMode: asset.StorageMode, ParseStatus: asset.ParseStatus, Truncated: asset.Truncated,
		NeedsColumn: plan.NeedsColumn,
		Rows:        []TableRowView{},
	}
	if result.Route == tableqa.RouteExplain {
		return s.explainTable(asset, result)
	}
	if s.columnarTable(asset) {
		return s.askTableColumnar(ctx, principal, knowledgeBaseID, asset, header, plan, input)
	}
	if asset.StorageMode != "" && asset.StorageMode != StorageModeFull {
		return TableAskResult{}, apperror.Invalidf(
			"table_rows_not_indexed: sheet %s is %s; use an external query for full rows",
			asset.Sheet, asset.StorageMode,
		)
	}
	rows, err := s.loadTableRows(ctx, principal, asset.ID)
	if err != nil {
		return TableAskResult{}, err
	}
	rows = filterTableRows(rows, input.Filters)
	switch result.Operation {
	case tableqa.OpRows:
		return lookupRows(question, rows, result, input.Limit)
	case tableqa.OpCount:
		return countRows(rows, result, input.Limit)
	case tableqa.OpSum, tableqa.OpAvg, tableqa.OpMin, tableqa.OpMax:
		return aggregateRows(rows, result, input.Limit)
	case tableqa.OpTop:
		return topRows(rows, result, input.Limit)
	default:
		return lookupRows(question, rows, result, input.Limit)
	}
}

func (s *Service) selectTableAsset(ctx context.Context, principal authz.Principal, knowledgeBaseID, question, assetID string) (models.TableAsset, error) {
	var assets []models.TableAsset
	query := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("knowledge_base_id = ?", knowledgeBaseID)
	if assetID != "" {
		query = query.Where("id = ?", assetID)
	}
	if err := query.Order("sheet, row_start, id").Find(&assets).Error; err != nil {
		return models.TableAsset{}, err
	}
	assets, err := s.currentVersionAssets(ctx, principal.TenantID, knowledgeBaseID, assets)
	if err != nil {
		return models.TableAsset{}, err
	}
	if len(assets) == 0 {
		return models.TableAsset{}, apperror.ErrNotFound
	}
	if assetID != "" || len(assets) == 1 {
		return assets[0], nil
	}
	text := strings.ToLower(question)
	bestScore := 0
	var best []models.TableAsset
	for _, asset := range assets {
		score := 0
		if sheet := strings.ToLower(strings.TrimSpace(asset.Sheet)); sheet != "" && strings.Contains(text, sheet) {
			score += 10
		}
		header := decodeHeader(asset.Header)
		score += 2 * len(tableqa.MatchColumns(question, header))
		switch {
		case score > bestScore:
			bestScore, best = score, []models.TableAsset{asset}
		case score > 0 && score == bestScore:
			best = append(best, asset)
		}
	}
	switch {
	case bestScore == 0:
		return models.TableAsset{}, apperror.Invalidf("table_asset_required: the question does not name a table; pass assetId or mention a sheet or column")
	case len(best) > 1:
		return models.TableAsset{}, apperror.Invalidf("table_asset_ambiguous: the question matches %d tables; pass assetId", len(best))
	default:
		return best[0], nil
	}
}

// currentVersionAssets drops assets left behind by an earlier document version.
// A reindex keeps the previous version reachable only through history, so table
// QA must not see both copies and report an ambiguity.
func (s *Service) currentVersionAssets(ctx context.Context, tenantID, knowledgeBaseID string, assets []models.TableAsset) ([]models.TableAsset, error) {
	var documents []models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("knowledge_base_id = ?", knowledgeBaseID).Find(&documents).Error; err != nil {
		return nil, err
	}
	latest := make(map[string]string, len(documents))
	for _, document := range documents {
		latest[document.ID] = document.LatestVersionID
	}
	filtered := make([]models.TableAsset, 0, len(assets))
	for _, asset := range assets {
		version, known := latest[asset.DocumentID]
		if known && version != "" && asset.DocumentVersionID != version {
			continue
		}
		filtered = append(filtered, asset)
	}
	return filtered, nil
}

func (s *Service) loadTableRows(ctx context.Context, principal authz.Principal, assetID string) ([]decodedTableRow, error) {
	var stored []models.TableRow
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("table_asset_id = ?", assetID).Order("row_number").Find(&stored).Error; err != nil {
		return nil, err
	}
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

type decodedTableRow struct {
	number int
	cells  map[string]string
}

func decodeHeader(raw string) []string {
	var header []string
	_ = json.Unmarshal([]byte(raw), &header)
	return header
}

func filterTableRows(rows []decodedTableRow, filters []TableFilter) []decodedTableRow {
	if len(filters) == 0 {
		return rows
	}
	filtered := make([]decodedTableRow, 0, len(rows))
	for _, row := range rows {
		if matchesFilters(row.cells, filters) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func lookupRows(question string, rows []decodedTableRow, result TableAskResult, limit int) (TableAskResult, error) {
	tokens := tableqa.Tokens(question)
	type scored struct {
		row   decodedTableRow
		score int
	}
	matches := make([]scored, 0, len(rows))
	for _, row := range rows {
		score := rowKeywordScore(tokens, row.cells)
		if score > 0 {
			matches = append(matches, scored{row: row, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].row.number < matches[j].row.number
	})
	result.MatchedRows = len(matches)
	limit = normalizeLimit(limit, 20, 200)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	for _, match := range matches {
		result.Rows = append(result.Rows, TableRowView{RowNumber: match.row.number, Cells: match.row.cells})
	}
	if len(matches) == 0 {
		result.Notes = append(result.Notes, "no row matched the question keywords")
	}
	return result, nil
}

func countRows(rows []decodedTableRow, result TableAskResult, limit int) (TableAskResult, error) {
	value := float64(len(rows))
	result.Value = &value
	result.MatchedRows = len(rows)
	result.Notes = append(result.Notes, "counted "+formatRows(len(rows)))
	limit = normalizeLimit(limit, 10, 50)
	for index, row := range rows {
		if index >= limit {
			break
		}
		result.Rows = append(result.Rows, TableRowView{RowNumber: row.number, Cells: row.cells})
	}
	return result, nil
}

func aggregateRows(rows []decodedTableRow, result TableAskResult, limit int) (TableAskResult, error) {
	if result.Column == "" || result.NeedsColumn {
		return TableAskResult{}, apperror.Invalidf("table_column_required: the question does not name a numeric column")
	}
	type valued struct {
		row   decodedTableRow
		value float64
	}
	values := make([]valued, 0, len(rows))
	for _, row := range rows {
		value, ok := tableqa.ParseNumber(row.cells[result.Column])
		if !ok {
			continue
		}
		values = append(values, valued{row: row, value: value})
	}
	if len(values) == 0 {
		return TableAskResult{}, apperror.Invalidf("table_column_not_numeric: column %s has no numeric values", result.Column)
	}
	var total float64
	minimum, maximum := values[0].value, values[0].value
	for _, item := range values {
		total += item.value
		if item.value < minimum {
			minimum = item.value
		}
		if item.value > maximum {
			maximum = item.value
		}
	}
	var answer float64
	switch result.Operation {
	case tableqa.OpSum:
		answer = total
	case tableqa.OpAvg:
		answer = total / float64(len(values))
	case tableqa.OpMin:
		answer = minimum
	case tableqa.OpMax:
		answer = maximum
	}
	result.Value = &answer
	result.MatchedRows = len(values)
	limit = normalizeLimit(limit, 10, 50)
	for index, item := range values {
		if index >= limit {
			break
		}
		result.Rows = append(result.Rows, TableRowView{RowNumber: item.row.number, Cells: item.row.cells})
	}
	result.Notes = append(result.Notes, descriptionForAggregate(result, len(values)))
	return result, nil
}

func topRows(rows []decodedTableRow, result TableAskResult, limit int) (TableAskResult, error) {
	if result.Column == "" || result.NeedsColumn {
		return TableAskResult{}, apperror.Invalidf("table_column_required: the question does not name a column to rank by")
	}
	type valued struct {
		row     decodedTableRow
		value   float64
		numeric bool
	}
	values := make([]valued, 0, len(rows))
	for _, row := range rows {
		value, ok := tableqa.ParseNumber(row.cells[result.Column])
		values = append(values, valued{row: row, value: value, numeric: ok})
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].numeric != values[j].numeric {
			return values[i].numeric
		}
		if values[i].numeric && values[i].value != values[j].value {
			if result.Desc {
				return values[i].value > values[j].value
			}
			return values[i].value < values[j].value
		}
		left := strings.ToLower(values[i].row.cells[result.Column])
		right := strings.ToLower(values[j].row.cells[result.Column])
		if left != right {
			if result.Desc {
				return left > right
			}
			return left < right
		}
		return values[i].row.number < values[j].row.number
	})
	topN := result.TopN
	if topN <= 0 {
		topN = 10
	}
	if limit := normalizeLimit(limit, topN, 100); limit < topN {
		topN = limit
	}
	if len(values) > topN {
		values = values[:topN]
	}
	for _, item := range values {
		result.Rows = append(result.Rows, TableRowView{RowNumber: item.row.number, Cells: item.row.cells})
	}
	result.MatchedRows = len(rows)
	order := "descending"
	if !result.Desc {
		order = "ascending"
	}
	result.Notes = append(result.Notes, "ordered by "+result.Column+" "+order)
	return result, nil
}

func (s *Service) explainTable(asset models.TableAsset, result TableAskResult) (TableAskResult, error) {
	result.MatchedRows = int(asset.ParsedRows)
	if result.MatchedRows == 0 {
		result.MatchedRows = asset.RowCount
	}
	var sample []string
	_ = json.Unmarshal([]byte(asset.SampleRows), &sample)
	if len(sample) > 5 {
		sample = sample[:5]
	}
	result.Sample = sample
	if asset.StorageMode != "" && asset.StorageMode != StorageModeFull {
		result.Notes = append(result.Notes, "storage_mode="+asset.StorageMode+": only metadata and sample rows are available; full rows need an external query")
	}
	if asset.Truncated {
		result.Notes = append(result.Notes, "the sheet was truncated during parsing")
	}
	if result.MatchedRows == 0 && len(result.Sample) == 0 {
		result.Notes = append(result.Notes, "no rows have been indexed for this sheet yet")
	}
	return result, nil
}

func rowKeywordScore(tokens []string, cells map[string]string) int {
	score := 0
	for _, token := range tokens {
		weight := len([]rune(token))
		for _, value := range cells {
			if strings.Contains(strings.ToLower(value), token) {
				score += weight
				break
			}
		}
	}
	return score
}

func normalizeLimit(limit, fallback, maximum int) int {
	if limit <= 0 {
		limit = fallback
	}
	if limit > maximum {
		limit = maximum
	}
	return limit
}

func descriptionForAggregate(result TableAskResult, numericCount int) string {
	switch result.Operation {
	case tableqa.OpSum:
		return "sum of " + result.Column + " over " + formatCount(numericCount) + " numeric cells"
	case tableqa.OpAvg:
		return "average of " + result.Column + " over " + formatCount(numericCount) + " numeric cells"
	case tableqa.OpMin:
		return "minimum of " + result.Column + " over " + formatCount(numericCount) + " numeric cells"
	case tableqa.OpMax:
		return "maximum of " + result.Column + " over " + formatCount(numericCount) + " numeric cells"
	default:
		return ""
	}
}

func formatCount(value int) string {
	if value == 1 {
		return "1 cell"
	}
	return strconv.Itoa(value) + " cells"
}

func formatRows(value int) string {
	if value == 1 {
		return "1 row"
	}
	return strconv.Itoa(value) + " rows"
}

func matchesFilters(cells map[string]string, filters []TableFilter) bool {
	for _, filter := range filters {
		value := strings.TrimSpace(cells[filter.Column])
		want := strings.TrimSpace(filter.Value)
		switch strings.ToLower(strings.TrimSpace(filter.Op)) {
		case "eq":
			if value != want {
				return false
			}
		case "ne":
			if value == want {
				return false
			}
		case "contains":
			if !strings.Contains(strings.ToLower(value), strings.ToLower(want)) {
				return false
			}
		case "gt", "gte", "lt", "lte":
			comparison := compareCells(value, want)
			switch strings.ToLower(filter.Op) {
			case "gt":
				if comparison <= 0 {
					return false
				}
			case "gte":
				if comparison < 0 {
					return false
				}
			case "lt":
				if comparison >= 0 {
					return false
				}
			case "lte":
				if comparison > 0 {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func compareCells(left, right string) int {
	leftNumber, leftErr := strconv.ParseFloat(strings.TrimSpace(left), 64)
	rightNumber, rightErr := strconv.ParseFloat(strings.TrimSpace(right), 64)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(strings.ToLower(left), strings.ToLower(right))
}
