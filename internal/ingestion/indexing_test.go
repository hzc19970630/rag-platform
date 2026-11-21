package ingestion

import (
	"context"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestIndexEstimateAndModeSwitch(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	service.SetIndexingBudget(600, 2)
	ctx := context.Background()
	principal := ownerPrincipal()
	if err := db.Create(&models.TableAsset{
		ID: "tbl-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-1",
		Sheet: "Sheet1", RowCount: 100, ColumnCount: 2,
	}).Error; err != nil {
		t.Fatalf("seed table asset: %v", err)
	}
	if err := db.Model(&models.DocumentVersion{}).Where("id = ?", "ver-1").Updates(map[string]any{
		"index_mode": IndexModeLexical, "total_rows": 100, "total_sheets": 1,
	}).Error; err != nil {
		t.Fatalf("seed version coverage: %v", err)
	}
	for index := 0; index < 4; index++ {
		if err := db.Create(&models.Chunk{
			ID: "chunk-est-" + string(rune('a'+index)), TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
			DocumentVersionID: "ver-1", ParentID: "parent-1", Ordinal: index, Content: "row data row data",
			ContentHash: "hash", Status: StatusEmbedded, IndexRole: IndexRoleKeyword,
		}).Error; err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
	estimate, err := service.IndexEstimate(ctx, principal, "doc-a")
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if estimate.IndexMode != IndexModeLexical || estimate.TotalRows != 100 || estimate.TableCount != 1 {
		t.Fatalf("estimate = %+v", estimate)
	}
	if estimate.DeepEstimatedTokens <= 0 || estimate.DeepEstimatedMinutes < 1 {
		t.Fatalf("estimate tokens/minutes = %+v", estimate)
	}
	if _, err := service.SetIndexMode(ctx, principal, "doc-a", "bogus"); err == nil {
		t.Fatalf("unsupported mode must fail")
	}
	if _, err := service.SetIndexMode(ctx, principal, "doc-a", IndexModeStandard); err != nil {
		t.Fatalf("switch mode: %v", err)
	}
	var document models.Document
	if err := db.First(&document, "id = ?", "doc-a").Error; err != nil {
		t.Fatalf("reload document: %v", err)
	}
	if document.IndexMode != IndexModeStandard {
		t.Fatalf("document index mode = %s", document.IndexMode)
	}
}
