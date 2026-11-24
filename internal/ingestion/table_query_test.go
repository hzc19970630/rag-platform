package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type fakeTableReader struct {
	result ColumnarTableResult
	err    error
	last   ColumnarTableQuery
	target TableMaterializeTarget
	sheet  string

	sqlResult ColumnarSQLResult
	sqlErr    error
	lastSQL   ColumnarSQLQuery
}

func (f *fakeTableReader) QueryTable(_ context.Context, target TableMaterializeTarget, sheet string, query ColumnarTableQuery) (ColumnarTableResult, error) {
	f.target = target
	f.sheet = sheet
	f.last = query
	return f.result, f.err
}

func (f *fakeTableReader) RunTableSQL(_ context.Context, target TableMaterializeTarget, sheet string, query ColumnarSQLQuery) (ColumnarSQLResult, error) {
	f.target = target
	f.sheet = sheet
	f.lastSQL = query
	return f.sqlResult, f.sqlErr
}

func TestQueryTableUsesColumnarBackend(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	reader := &fakeTableReader{result: ColumnarTableResult{
		MatchedRows: 2,
		Rows: []TableRowView{
			{RowNumber: 2, Cells: map[string]string{"name": "tape", "qty": "5"}},
			{RowNumber: 4, Cells: map[string]string{"name": "box", "qty": "3"}},
		},
	}}
	service.SetTablehouse(reader)
	if err := db.Create(&models.TableAsset{
		ID: "asset-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name","qty"]`, RowCount: 3, ColumnCount: 2,
		StorageURI: "tablehouse/tenant=tenant-a/kb=kb-a/doc=doc-a/ver=ver-1/sheet=Sheet1", StorageFormat: "parquet",
		MaterializedRows: 3, MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	result, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{
		AssetID: "asset-1", Filters: []TableFilter{{Column: "qty", Op: "gte", Value: "3"}},
		OrderBy: "qty", Desc: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if result.MatchedRows != 2 || len(result.Rows) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Header[0] != "name" || result.Header[1] != "qty" {
		t.Fatalf("header = %v", result.Header)
	}
	if result.SourceRows != 0 {
		t.Fatalf("source rows = %d", result.SourceRows)
	}
	if reader.sheet != "Sheet1" || reader.target.DocumentID != "doc-a" || reader.target.VersionID != "ver-1" {
		t.Fatalf("backend target = %+v sheet=%q", reader.target, reader.sheet)
	}
	if len(reader.last.Filters) != 1 || reader.last.Filters[0].Column != "qty" || reader.last.OrderBy != "qty" || !reader.last.Desc || reader.last.Limit != 10 {
		t.Fatalf("backend query = %+v", reader.last)
	}
}

func TestQueryTableRejectsLegacyAndFailedAssets(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	service.SetTablehouse(&fakeTableReader{})
	for _, asset := range []models.TableAsset{
		{ID: "asset-legacy", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1", Sheet: "Sheet1", Header: `["name"]`, RowCount: 1},
		{
			ID: "asset-failed", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
			Sheet: "huge", Header: `["name"]`, RowCount: 10, MaterializeStatus: MaterializeStatusFailed, MaterializeError: "sheet row limit exceeded",
		},
	} {
		if err := db.Create(&asset).Error; err != nil {
			t.Fatalf("seed asset %s: %v", asset.ID, err)
		}
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-legacy"}); err == nil || !strings.Contains(err.Error(), "table_needs_reparse") {
		t.Fatalf("legacy query error = %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-failed"}); err == nil || !strings.Contains(err.Error(), "table_materialize_failed") {
		t.Fatalf("failed query error = %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-other", TableQuery{AssetID: "asset-legacy"}); err == nil {
		t.Fatalf("cross-knowledge-base query must fail")
	}
}

func TestQueryTableWithoutBackendIsNotReady(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	if err := db.Create(&models.TableAsset{
		ID: "asset-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1, StorageURI: "tablehouse/x", MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-1"}); !errors.Is(err, apperror.ErrNotReady) {
		t.Fatalf("query without backend error = %v", err)
	}
}

func TestQueryTableHidesSoftDeletedDocument(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	service.SetTablehouse(&fakeTableReader{result: ColumnarTableResult{
		MatchedRows: 1,
		Rows:        []TableRowView{{RowNumber: 2, Cells: map[string]string{"name": "pen"}}},
	}})
	if err := db.Create(&models.TableAsset{
		ID: "asset-deleted", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1, ColumnCount: 1,
		StorageURI: "tablehouse/tenant=tenant-a/kb=kb-a/doc=doc-a/ver=ver-1/sheet=Sheet1", MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-deleted"}); err != nil {
		t.Fatalf("live query: %v", err)
	}
	deletedAt := time.Now()
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Updates(map[string]any{
		"status": StatusCancelled, "deleted_at": &deletedAt,
	}).Error; err != nil {
		t.Fatalf("soft delete document: %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-deleted"}); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("soft-deleted query error = %v", err)
	}
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Update("deleted_at", nil).Error; err != nil {
		t.Fatalf("restore document: %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-deleted"}); err != nil {
		t.Fatalf("restored query: %v", err)
	}
}

func TestQueryTableSQLAuditsPreviewAndExecute(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	reader := &fakeTableReader{sqlResult: ColumnarSQLResult{
		Columns:      []TableSQLColumn{{Name: "total", Type: "NUMBER"}},
		Rows:         [][]any{{3}},
		RowCount:     1,
		ReturnedRows: 1,
		ElapsedMs:    9,
	}}
	service.SetTablehouse(reader)
	if err := db.Create(&models.TableAsset{
		ID: "asset-sql", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name","qty"]`, RowCount: 3, ColumnCount: 2,
		StorageURI: "tablehouse/tenant=tenant-a/kb=kb-a/doc=doc-a/ver=ver-1/sheet=Sheet1", StorageFormat: "parquet",
		MaterializedRows: 3, MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	result, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{
		AssetID: "asset-sql", SQL: ` SELECT count(*) AS total FROM t `, Params: []any{"pen"}, Preview: true, Limit: 9999,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if result.AssetID != "asset-sql" || result.Sheet != "Sheet1" || !result.Preview || result.RowCount != 1 || result.ReturnedRows != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.SQL != `SELECT count(*) AS total FROM t` {
		t.Fatalf("sql = %q", result.SQL)
	}
	if reader.sheet != "Sheet1" || reader.target.DocumentID != "doc-a" || reader.target.VersionID != "ver-1" {
		t.Fatalf("target = %+v sheet=%q", reader.target, reader.sheet)
	}
	if !reader.lastSQL.Preview || reader.lastSQL.Limit != maxTableSQLLimit || len(reader.lastSQL.Params) != 1 {
		t.Fatalf("backend query = %+v", reader.lastSQL)
	}
	var audits []models.AuditLog
	if err := db.Where("action = ? AND resource_id = ?", "table_query", "asset-sql").Order("id").Find(&audits).Error; err != nil {
		t.Fatalf("load audits: %v", err)
	}
	if len(audits) != 1 || !audits[0].Allowed || audits[0].ResourceType != "table_asset" || !strings.Contains(audits[0].Reason, "preview") {
		t.Fatalf("audits = %+v", audits)
	}
	if _, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{AssetID: "asset-sql", SQL: "SELECT 1"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if reader.lastSQL.Preview || reader.lastSQL.Limit != defaultTableSQLLimit {
		t.Fatalf("execute query = %+v", reader.lastSQL)
	}
}

func TestQueryTableSQLRejectionIsAudited(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	reader := &fakeTableReader{sqlErr: &TableSheetError{Status: 400, Message: "only SELECT statements are allowed"}}
	service.SetTablehouse(reader)
	if err := db.Create(&models.TableAsset{
		ID: "asset-sql", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1,
		StorageURI: "tablehouse/tenant=tenant-a/kb=kb-a/doc=doc-a/ver=ver-1/sheet=Sheet1", MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	_, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{AssetID: "asset-sql", SQL: "DELETE FROM t"})
	if err == nil || !strings.Contains(err.Error(), "table_sql_rejected") {
		t.Fatalf("error = %v", err)
	}
	var audit models.AuditLog
	if err := db.First(&audit, "action = ? AND resource_id = ?", "table_query", "asset-sql").Error; err != nil {
		t.Fatalf("load audit: %v", err)
	}
	if audit.Allowed || !strings.Contains(audit.Reason, "rejected") || !strings.Contains(audit.Reason, "DELETE FROM t") {
		t.Fatalf("audit = %+v", audit)
	}
}

func TestQueryTableSQLValidatesInputBeforeBackend(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	reader := &fakeTableReader{}
	service.SetTablehouse(reader)
	if err := db.Create(&models.TableAsset{
		ID: "asset-sql", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1,
		StorageURI: "tablehouse/x", MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if err := db.Create(&models.TableAsset{
		ID: "asset-legacy", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1,
	}).Error; err != nil {
		t.Fatalf("seed legacy asset: %v", err)
	}
	if _, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{AssetID: "asset-sql", SQL: "   "}); err == nil {
		t.Fatalf("empty sql must fail")
	}
	params := make([]any, maxTableSQLParams+1)
	if _, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{AssetID: "asset-sql", SQL: "SELECT 1", Params: params}); err == nil {
		t.Fatalf("too many params must fail")
	}
	if reader.lastSQL.SQL != "" {
		t.Fatalf("backend must not be called: %+v", reader.lastSQL)
	}
	if _, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{AssetID: "asset-legacy", SQL: "SELECT 1"}); err == nil || !strings.Contains(err.Error(), "table_needs_reparse") {
		t.Fatalf("legacy error = %v", err)
	}
	if _, err := service.QueryTableSQL(ctx, principal, "kb-other", TableSQLQuery{AssetID: "asset-sql", SQL: "SELECT 1"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("cross-knowledge-base error = %v", err)
	}
}

func TestQueryTableSQLWithoutBackendIsNotReady(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	if err := db.Create(&models.TableAsset{
		ID: "asset-sql", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1, StorageURI: "tablehouse/x", MaterializeStatus: MaterializeStatusReady,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if _, err := service.QueryTableSQL(ctx, principal, "kb-a", TableSQLQuery{AssetID: "asset-sql", SQL: "SELECT 1"}); !errors.Is(err, apperror.ErrNotReady) {
		t.Fatalf("error = %v", err)
	}
}

func TestListTableAssetsHidesSoftDeletedDocument(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	now := time.Now()
	if err := db.Create(&models.Document{
		ID: "doc-b", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "user-a",
		Filename: "other.xlsx", MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Status: StatusEvidenceReady, LatestVersionID: "ver-b", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed live document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-b", DocumentID: "doc-b", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		Version: 1, ContentHash: "hash-b", SizeBytes: 1, SourceObjectKey: "source-b",
		Status: StatusEvidenceReady, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed live version: %v", err)
	}
	for _, asset := range []models.TableAsset{
		{ID: "asset-deleted", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1", Sheet: "Sheet1"},
		{ID: "asset-live", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-b", DocumentVersionID: "ver-b", Sheet: "Sheet1"},
	} {
		if err := db.Create(&asset).Error; err != nil {
			t.Fatalf("seed asset %s: %v", asset.ID, err)
		}
	}
	deletedAt := now
	if err := db.Model(&models.Document{}).Where("id = ?", "doc-a").Updates(map[string]any{
		"status": StatusCancelled, "deleted_at": &deletedAt,
	}).Error; err != nil {
		t.Fatalf("soft delete document: %v", err)
	}
	assets, err := service.ListTableAssets(ctx, principal, "kb-a")
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(assets) != 1 || assets[0].ID != "asset-live" {
		t.Fatalf("assets = %+v", assets)
	}
}
