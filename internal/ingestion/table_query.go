package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
)

const (
	defaultTableSQLLimit = 100
	maxTableSQLLimit     = 500
	maxTableSQLLength    = 8000
	maxTableSQLParams    = 32
	maxAuditSQLRunes     = 2000
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

// ColumnarTableReader executes filter plans and read-only SQL over the
// Parquet copy.
type ColumnarTableReader interface {
	QueryTable(ctx context.Context, target TableMaterializeTarget, sheet string, query ColumnarTableQuery) (ColumnarTableResult, error)
	RunTableSQL(ctx context.Context, target TableMaterializeTarget, sheet string, query ColumnarSQLQuery) (ColumnarSQLResult, error)
}

// TableSQLQuery is a bounded, read-only statement over one materialized sheet.
// The tablehouse analyzer rejects writes, multiple statements and unknown
// relations; the API only forwards validated identifiers and limits.
type TableSQLQuery struct {
	AssetID string `json:"assetId"`
	SQL     string `json:"sql"`
	Params  []any  `json:"params,omitempty"`
	Preview bool   `json:"preview,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type TableSQLColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type TableSQLResult struct {
	AssetID      string           `json:"assetId"`
	Sheet        string           `json:"sheet"`
	SQL          string           `json:"sql"`
	Preview      bool             `json:"preview"`
	Columns      []TableSQLColumn `json:"columns"`
	Rows         [][]any          `json:"rows"`
	RowCount     int              `json:"rowCount"`
	ReturnedRows int              `json:"returnedRows"`
	Truncated    bool             `json:"truncated"`
	ElapsedMs    int64            `json:"elapsedMs"`
}

type ColumnarSQLQuery struct {
	SQL     string
	Params  []any
	Preview bool
	Limit   int
}

type ColumnarSQLResult struct {
	Columns      []TableSQLColumn
	Rows         [][]any
	RowCount     int
	ReturnedRows int
	Truncated    bool
	ElapsedMs    int64
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
	asset, err := s.loadLiveTableAsset(ctx, principal, knowledgeBaseID, query.AssetID)
	if err != nil {
		return TableQueryResult{}, err
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

// loadLiveTableAsset resolves a table asset inside one knowledge base and
// rejects failed or pre-B39 sheets before any backend call.
func (s *Service) loadLiveTableAsset(ctx context.Context, principal authz.Principal, knowledgeBaseID, assetID string) (models.TableAsset, error) {
	var asset models.TableAsset
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("id = ? AND knowledge_base_id = ?", assetID, knowledgeBaseID).
		Where(liveTableAsset).First(&asset).Error; err != nil {
		return models.TableAsset{}, apperror.ErrNotFound
	}
	if asset.MaterializeStatus == MaterializeStatusFailed {
		message := asset.MaterializeError
		if message == "" {
			message = "materialization failed"
		}
		return models.TableAsset{}, apperror.Invalidf("table_materialize_failed: sheet %s: %s", asset.Sheet, message)
	}
	if asset.StorageURI == "" {
		return models.TableAsset{}, apperror.Invalidf("table_needs_reparse: sheet %s has no columnar copy; reparse the document", asset.Sheet)
	}
	return asset, nil
}

// QueryTableSQL runs one read-only statement over a materialized sheet. The
// statement is analyzed by tablehouse (single SELECT, relation t, bounded rows,
// timeout and memory); every accepted or rejected execution is audited.
func (s *Service) QueryTableSQL(ctx context.Context, principal authz.Principal, knowledgeBaseID string, query TableSQLQuery) (TableSQLResult, error) {
	started := time.Now()
	result, err := s.queryTableSQL(ctx, principal, knowledgeBaseID, query)
	if s.metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		mode := "execute"
		if query.Preview {
			mode = "preview"
		}
		s.metrics.Inc("ingestion_table_sql_query_total", map[string]string{"status": status, "mode": mode})
		s.metrics.Observe("ingestion_table_sql_query_duration", time.Since(started), map[string]string{"mode": mode})
	}
	return result, err
}

func (s *Service) queryTableSQL(ctx context.Context, principal authz.Principal, knowledgeBaseID string, query TableSQLQuery) (TableSQLResult, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return TableSQLResult{}, err
	}
	sql := strings.TrimSpace(query.SQL)
	if sql == "" {
		return TableSQLResult{}, apperror.Invalidf("sql is required")
	}
	if len(sql) > maxTableSQLLength {
		return TableSQLResult{}, apperror.Invalidf("sql exceeds %d characters", maxTableSQLLength)
	}
	if len(query.Params) > maxTableSQLParams {
		return TableSQLResult{}, apperror.Invalidf("at most %d parameters are allowed", maxTableSQLParams)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultTableSQLLimit
	}
	if limit > maxTableSQLLimit {
		limit = maxTableSQLLimit
	}
	asset, err := s.loadLiveTableAsset(ctx, principal, knowledgeBaseID, query.AssetID)
	if err != nil {
		return TableSQLResult{}, err
	}
	if s.tables == nil {
		return TableSQLResult{}, fmt.Errorf("%w: table SQL backend is not configured", apperror.ErrNotReady)
	}
	backend, err := s.tables.RunTableSQL(ctx, TableMaterializeTarget{
		TenantID: asset.TenantID, KnowledgeBaseID: asset.KnowledgeBaseID,
		DocumentID: asset.DocumentID, VersionID: asset.DocumentVersionID,
	}, asset.Sheet, ColumnarSQLQuery{SQL: sql, Params: query.Params, Preview: query.Preview, Limit: limit})
	if err != nil {
		_ = s.recordTableSQLAudit(ctx, principal, asset.ID, false, tableSQLAuditReason(query.Preview, err.Error(), sql))
		var sheetErr *TableSheetError
		if errors.As(err, &sheetErr) {
			return TableSQLResult{}, apperror.Invalidf("table_sql_rejected: %s", sheetErr.Message)
		}
		return TableSQLResult{}, err
	}
	if err := s.recordTableSQLAudit(ctx, principal, asset.ID, true, tableSQLAuditReason(query.Preview, "", sql)); err != nil {
		return TableSQLResult{}, err
	}
	columns := backend.Columns
	if columns == nil {
		columns = []TableSQLColumn{}
	}
	rows := backend.Rows
	if rows == nil {
		rows = [][]any{}
	}
	return TableSQLResult{
		AssetID: asset.ID, Sheet: asset.Sheet, SQL: sql, Preview: query.Preview,
		Columns: columns, Rows: rows, RowCount: backend.RowCount, ReturnedRows: backend.ReturnedRows,
		Truncated: backend.Truncated, ElapsedMs: backend.ElapsedMs,
	}, nil
}

func (s *Service) recordTableSQLAudit(ctx context.Context, principal authz.Principal, assetID string, allowed bool, reason string) error {
	if err := s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "table_query",
		ResourceType: "table_asset", ResourceID: assetID, Allowed: allowed, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error; err != nil {
		return fmt.Errorf("record table query audit: %w", err)
	}
	return nil
}

func tableSQLAuditReason(preview bool, failure, sql string) string {
	mode := "execute"
	if preview {
		mode = "preview"
	}
	reason := mode + ": " + sql
	if failure != "" {
		reason = mode + " rejected (" + failure + "): " + sql
	}
	runes := []rune(reason)
	if len(runes) > maxAuditSQLRunes {
		return string(runes[:maxAuditSQLRunes])
	}
	return reason
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
