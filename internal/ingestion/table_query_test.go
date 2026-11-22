package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestQueryTableFiltersAndSorts(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	if err := db.Create(&models.TableAsset{
		ID: "asset-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name","qty"]`, RowCount: 3, ColumnCount: 2,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	rows := []map[string]string{
		{"name": "pen", "qty": "2"},
		{"name": "tape", "qty": "5"},
		{"name": "box", "qty": "3"},
	}
	for index, cells := range rows {
		encoded, _ := json.Marshal(cells)
		if err := db.Create(&models.TableRow{
			ID: "row-" + string(rune('a'+index)), TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentID: "doc-a", DocumentVersionID: "ver-1", TableAssetID: "asset-1",
			RowNumber: index + 2, Cells: string(encoded),
		}).Error; err != nil {
			t.Fatalf("seed row: %v", err)
		}
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
	if result.Rows[0].Cells["name"] != "tape" || result.Rows[1].Cells["name"] != "box" {
		t.Fatalf("rows = %+v", result.Rows)
	}
	if result.Header[0] != "name" || result.Header[1] != "qty" {
		t.Fatalf("header = %v", result.Header)
	}
	limited, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-1", Limit: 1})
	if err != nil || len(limited.Rows) != 1 || limited.MatchedRows != 3 {
		t.Fatalf("limited = %+v, %v", limited, err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-other", TableQuery{AssetID: "asset-1"}); err == nil {
		t.Fatalf("cross-knowledge-base query must fail")
	}
	if err := db.Create(&models.TableAsset{
		ID: "asset-sampled", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "huge", Header: `["name","qty"]`, RowCount: 10, TotalRows: 500000, StorageMode: StorageModeSampled,
	}).Error; err != nil {
		t.Fatalf("seed sampled asset: %v", err)
	}
	if _, err := service.QueryTable(ctx, principal, "kb-a", TableQuery{AssetID: "asset-sampled"}); err == nil || !strings.Contains(err.Error(), "table_rows_not_indexed") {
		t.Fatalf("sampled query error = %v", err)
	}
}

func TestQueryTableHidesSoftDeletedDocument(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	ctx := context.Background()
	principal := ownerPrincipal()
	if err := db.Create(&models.TableAsset{
		ID: "asset-deleted", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", Header: `["name"]`, RowCount: 1, ColumnCount: 1,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	if err := db.Create(&models.TableRow{
		ID: "row-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a",
		DocumentVersionID: "ver-1", TableAssetID: "asset-deleted", RowNumber: 2, Cells: `{"name":"pen"}`,
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
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
