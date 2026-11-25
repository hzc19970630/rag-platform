package ingestion

import (
	"context"
	"encoding/json"
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

// AskTable classifies a table question and executes it over the B39 columnar
// copy: lookup filters rows, aggregate counts or computes one column, and
// explain returns table metadata without scanning rows.
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
	if asset.MaterializeStatus == MaterializeStatusFailed {
		message := asset.MaterializeError
		if message == "" {
			message = "materialization failed"
		}
		return TableAskResult{}, apperror.Invalidf("table_materialize_failed: sheet %s: %s", asset.Sheet, message)
	}
	if strings.TrimSpace(asset.StorageURI) == "" {
		return TableAskResult{}, apperror.Invalidf("table_needs_reparse: sheet %s has no columnar copy; reparse the document", asset.Sheet)
	}
	return s.askTableColumnar(ctx, principal, knowledgeBaseID, asset, header, plan, input)
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

func decodeHeader(raw string) []string {
	var header []string
	_ = json.Unmarshal([]byte(raw), &header)
	return header
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
