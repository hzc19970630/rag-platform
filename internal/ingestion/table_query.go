package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
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

// ColumnarTableQuery is the server-built filter plan sent to tablehouse. User
// SQL is not part of P0.
type ColumnarTableQuery struct {
	Filters []TableFilter `json:"filters,omitempty"`
	OrderBy string        `json:"orderBy,omitempty"`
	Desc    bool          `json:"desc,omitempty"`
	Limit   int           `json:"limit"`
}

// ColumnarTableResult is the backend's answer. TotalRows comes from the sheet
// manifest and is informational; the handler uses the asset metadata.
type ColumnarTableResult struct {
	Rows        []TableRowView
	MatchedRows int
	TotalRows   int
}

// ColumnarTableReader executes filter plans over the Parquet copy.
type ColumnarTableReader interface {
	QueryTable(ctx context.Context, target TableMaterializeTarget, sheet string, query ColumnarTableQuery) (ColumnarTableResult, error)
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

// liveTableAsset confines table reads to documents that are not soft deleted.
// Table assets survive the retention window so a restore brings them back, but
// they must stay invisible to read APIs until then.
const liveTableAsset = "EXISTS (SELECT 1 FROM documents d WHERE d.id = table_assets.document_id AND d.deleted_at IS NULL)"

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
		Where("id = ? AND knowledge_base_id = ?", query.AssetID, knowledgeBaseID).
		Where(liveTableAsset).First(&asset).Error; err != nil {
		return TableQueryResult{}, apperror.ErrNotFound
	}
	if asset.MaterializeStatus == MaterializeStatusFailed {
		message := asset.MaterializeError
		if message == "" {
			message = "materialization failed"
		}
		return TableQueryResult{}, apperror.Invalidf("table_materialize_failed: sheet %s: %s", asset.Sheet, message)
	}
	if asset.StorageURI == "" {
		return TableQueryResult{}, apperror.Invalidf("table_needs_reparse: sheet %s has no columnar copy; reparse the document", asset.Sheet)
	}
	if s.tables == nil {
		return TableQueryResult{}, fmt.Errorf("%w: table query backend is not configured", apperror.ErrNotReady)
	}
	var header []string
	_ = json.Unmarshal([]byte(asset.Header), &header)
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	backend, err := s.tables.QueryTable(ctx, TableMaterializeTarget{
		TenantID: asset.TenantID, KnowledgeBaseID: asset.KnowledgeBaseID,
		DocumentID: asset.DocumentID, VersionID: asset.DocumentVersionID,
	}, asset.Sheet, ColumnarTableQuery{Filters: query.Filters, OrderBy: query.OrderBy, Desc: query.Desc, Limit: limit})
	if err != nil {
		return TableQueryResult{}, err
	}
	total := int(asset.MaterializedRows)
	if total == 0 {
		total = int(asset.RowCount)
	}
	rows := backend.Rows
	if rows == nil {
		rows = []TableRowView{}
	}
	return TableQueryResult{
		AssetID: asset.ID, Sheet: asset.Sheet, Header: header,
		TotalRows: total, MatchedRows: backend.MatchedRows, Rows: rows,
		SourceRows: asset.TotalRows, StorageMode: asset.StorageMode,
		ParseStatus: asset.ParseStatus, Truncated: asset.Truncated,
	}, nil
}

// ListTableAssets returns the parsed table schemas for one knowledge base.
func (s *Service) ListTableAssets(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]models.TableAsset, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var assets []models.TableAsset
	err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("knowledge_base_id = ?", knowledgeBaseID).
		Where(liveTableAsset).Order("sheet, row_start, id").Find(&assets).Error
	return assets, err
}
