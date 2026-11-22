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
)

type TableFilter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

type TableQuery struct {
	AssetID string        `json:"assetId"`
	Filters []TableFilter `json:"filters"`
	OrderBy string        `json:"orderBy"`
	Desc    bool          `json:"desc"`
	Limit   int           `json:"limit"`
}

type TableRowView struct {
	RowNumber int               `json:"rowNumber"`
	Cells     map[string]string `json:"cells"`
}

type TableQueryResult struct {
	AssetID     string         `json:"assetId"`
	Sheet       string         `json:"sheet"`
	Header      []string       `json:"header"`
	TotalRows   int            `json:"totalRows"`
	MatchedRows int            `json:"matchedRows"`
	Rows        []TableRowView `json:"rows"`
	SourceRows  int64          `json:"sourceRows,omitempty"`
	StorageMode string         `json:"storageMode,omitempty"`
	ParseStatus string         `json:"parseStatus,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
}

// QueryTable answers deterministic filters and TopN over a table asset. It does
// not call a model: natural-language aggregation/Text-to-SQL is a later step.
func (s *Service) QueryTable(ctx context.Context, principal authz.Principal, knowledgeBaseID string, query TableQuery) (TableQueryResult, error) {
	started := time.Now()
	result, err := s.queryTable(ctx, principal, knowledgeBaseID, query)
	if s.metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		s.metrics.Inc("ingestion_table_query_total", map[string]string{"status": status})
		s.metrics.Observe("ingestion_table_query_duration", time.Since(started), nil)
	}
	return result, err
}

func (s *Service) queryTable(ctx context.Context, principal authz.Principal, knowledgeBaseID string, query TableQuery) (TableQueryResult, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return TableQueryResult{}, err
	}
	var asset models.TableAsset
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("id = ? AND knowledge_base_id = ?", query.AssetID, knowledgeBaseID).First(&asset).Error; err != nil {
		return TableQueryResult{}, apperror.ErrNotFound
	}
	if asset.StorageMode != "" && asset.StorageMode != StorageModeFull {
		return TableQueryResult{}, apperror.Invalidf(
			"table_rows_not_indexed: sheet %s is %s; use an external query for full rows",
			asset.Sheet, asset.StorageMode,
		)
	}
	var rows []models.TableRow
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("table_asset_id = ?", asset.ID).Order("row_number").Find(&rows).Error; err != nil {
		return TableQueryResult{}, err
	}
	var header []string
	_ = json.Unmarshal([]byte(asset.Header), &header)
	filtered := make([]TableRowView, 0, len(rows))
	for _, row := range rows {
		cells := map[string]string{}
		if err := json.Unmarshal([]byte(row.Cells), &cells); err != nil {
			continue
		}
		if !matchesFilters(cells, query.Filters) {
			continue
		}
		filtered = append(filtered, TableRowView{RowNumber: row.RowNumber, Cells: cells})
	}
	if query.OrderBy != "" {
		desc := query.Desc
		sort.SliceStable(filtered, func(i, j int) bool {
			less := compareCells(filtered[i].Cells[query.OrderBy], filtered[j].Cells[query.OrderBy]) < 0
			if desc {
				return !less
			}
			return less
		})
	}
	totalMatched := len(filtered)
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return TableQueryResult{
		AssetID: asset.ID, Sheet: asset.Sheet, Header: header,
		TotalRows: int(asset.RowCount), MatchedRows: totalMatched, Rows: filtered,
		SourceRows: asset.TotalRows, StorageMode: asset.StorageMode,
		ParseStatus: asset.ParseStatus, Truncated: asset.Truncated,
	}, nil
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

// ListTableAssets returns the parsed table schemas for one knowledge base.
func (s *Service) ListTableAssets(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]models.TableAsset, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var assets []models.TableAsset
	err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("knowledge_base_id = ?", knowledgeBaseID).Order("sheet, row_start, id").Find(&assets).Error
	return assets, err
}
