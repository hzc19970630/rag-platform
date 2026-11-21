package knowledgeprojection

import (
	"context"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/status"
)

func TestBuildGenerationCarriesForwardAndActivates(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-1", IdempotencyKey: "package:pkg-1",
	}); err != nil {
		t.Fatalf("record change: %v", err)
	}
	if err := db.Model(&models.WikiPage{}).Where("id = ?", "wp1").Updates(map[string]any{
		"status": PagePublished, "published_version_id": "wv1", "current_version_id": "wv1", "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("publish page: %v", err)
	}
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "kir-wv1", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1",
		Title: "Page", Content: "content", Status: IndexActive, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed index record: %v", err)
	}
	generation0 := models.WikiGeneration{
		ID: "gen-0", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 0, Status: status.Active,
		TargetEpoch: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&generation0).Error; err != nil {
		t.Fatalf("seed generation 0: %v", err)
	}
	if err := db.Create(&models.WikiGenerationPage{
		TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-0", WikiPageID: "wp1",
		WikiPageVersionID: stringPtr("wv1"), Status: status.GenerationPageCarried, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed generation page: %v", err)
	}
	if err := db.Model(&models.KnowledgeProjectionState{}).Where("knowledge_base_id = ?", "kb1").
		Updates(map[string]any{"active_wiki_generation_id": "gen-0", "wiki_applied_epoch": 1}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	run := models.ProjectionRun{
		ID: "run-gen", TenantID: "t1", KnowledgeBaseID: "kb1", ProjectionType: ProjectionTypeWiki,
		Status: ProjectionRunning, TargetEpoch: 1, BaseEpoch: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	knowledgeBase := models.KnowledgeBase{ID: "kb1", TenantID: "t1", OwnerID: "u1", Name: "KB", ACLVersion: 1}
	if activated, err := service.buildGeneration(ctx, &run, knowledgeBase, "hash"); err != nil {
		t.Fatalf("build generation: %v", err)
	} else if !activated {
		t.Fatalf("unchanged page generation must activate")
	}
	var active models.WikiGeneration
	if err := db.First(&active, "tenant_id = ? AND knowledge_base_id = ? AND status = ?", "t1", "kb1", status.Active).Error; err != nil {
		t.Fatalf("load active generation: %v", err)
	}
	if active.ID == "gen-0" || active.GenerationNo != 1 {
		t.Fatalf("active generation = %s no=%d", active.ID, active.GenerationNo)
	}
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.PublishedVersionID == nil || *page.PublishedVersionID != "wv1" || page.Status != PagePublished {
		t.Fatalf("carried page = %+v", page)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "knowledge_base_id = ?", "kb1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.ActiveWikiGenerationID == nil || *state.ActiveWikiGenerationID != active.ID {
		t.Fatalf("state active generation = %v", state.ActiveWikiGenerationID)
	}
}

func TestRollbackGenerationRestoresEarlierPages(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Model(&models.WikiPage{}).Where("id = ?", "wp1").Updates(map[string]any{
		"status": PagePublished, "current_version_id": "wv2", "published_version_id": "wv2", "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("publish page: %v", err)
	}
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Updates(map[string]any{
		"content": "old", "review_status": ReviewApproved, "source_hash": "h1",
	}).Error; err != nil {
		t.Fatalf("update version 1: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv2", WikiPageID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 2, Content: "new", SourceHash: "h2", ReviewStatus: ReviewApproved, ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version 2: %v", err)
	}
	generations := []models.WikiGeneration{
		{ID: "gen-old", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 1, Status: status.Superseded, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gen-new", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 2, Status: status.Active, BaseGenerationID: stringPtr("gen-old"), TargetEpoch: 2, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&generations).Error; err != nil {
		t.Fatalf("seed generations: %v", err)
	}
	if err := db.Create(&[]models.WikiGenerationPage{
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-old", WikiPageID: "wp1", WikiPageVersionID: stringPtr("wv1"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-new", WikiPageID: "wp1", WikiPageVersionID: stringPtr("wv2"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed generation pages: %v", err)
	}
	if err := db.Create(&[]models.KnowledgeIndexRecord{
		{ID: "kir-old", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1", Title: "Page", Content: "old", Status: IndexActive, CreatedAt: now, UpdatedAt: now},
		{ID: "kir-new", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv2", Title: "Page", Content: "new", Status: IndexActive, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed index records: %v", err)
	}
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", ActiveWikiGenerationID: stringPtr("gen-new"),
		TargetEpoch: 2, WikiAppliedEpoch: 2, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.RollbackGeneration(ctx, principal, "kb1", "gen-old"); err != nil {
		t.Fatalf("rollback generation: %v", err)
	}
	var old, current models.WikiGeneration
	if err := db.First(&old, "id = ?", "gen-old").Error; err != nil {
		t.Fatalf("load old generation: %v", err)
	}
	if err := db.First(&current, "id = ?", "gen-new").Error; err != nil {
		t.Fatalf("load current generation: %v", err)
	}
	if old.Status != status.Active || current.Status != status.Superseded {
		t.Fatalf("generation statuses = %s/%s", old.Status, current.Status)
	}
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.PublishedVersionID == nil || *page.PublishedVersionID != "wv1" {
		t.Fatalf("page published version = %v, want wv1", page.PublishedVersionID)
	}
	var records []models.KnowledgeIndexRecord
	if err := db.Where("knowledge_base_id = ?", "kb1").Find(&records).Error; err != nil {
		t.Fatalf("load index records: %v", err)
	}
	if len(records) != 1 || records[0].WikiPageVersionID != "wv1" {
		t.Fatalf("index records = %+v", records)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "knowledge_base_id = ?", "kb1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.ActiveWikiGenerationID == nil || *state.ActiveWikiGenerationID != "gen-old" || state.WikiAppliedEpoch != 1 {
		t.Fatalf("state = %+v", state)
	}
}

func stringPtr(value string) *string { return &value }
