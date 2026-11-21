package knowledgeprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/projectioncache"
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
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "kir-old", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1", Title: "Page", Content: "old", Status: IndexActive, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed index records: %v", err)
	}
	// wp2 only exists in the newer generation; rollback must archive it.
	if err := db.Create(&models.WikiPage{
		ID: "wp2", TenantID: "t1", KnowledgeBaseID: "kb1", PageType: "document", Slug: "second",
		Title: "Second", Status: PagePublished, CurrentVersionID: stringPtr("wv3"), PublishedVersionID: stringPtr("wv3"),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed second page: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv3", WikiPageID: "wp2", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 1, Content: "second",
		SourceHash: "h3", ReviewStatus: ReviewApproved, ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed second version: %v", err)
	}
	if err := db.Create(&models.WikiGenerationPage{
		TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-new", WikiPageID: "wp2",
		WikiPageVersionID: stringPtr("wv3"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed second mapping: %v", err)
	}
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "kir-wv3", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp2", WikiPageVersionID: "wv3", Title: "Second", Content: "second", Status: IndexActive, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed second index: %v", err)
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
	var archived models.WikiPage
	if err := db.First(&archived, "id = ?", "wp2").Error; err != nil {
		t.Fatalf("load second page: %v", err)
	}
	if archived.Status != PageArchived {
		t.Fatalf("second page = %s, want ARCHIVED", archived.Status)
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

func TestNoteEvidenceChangeDedupesPackage(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	if err := service.NoteEvidenceChange(ctx, "t1", "kb1", "pkg-1"); err != nil {
		t.Fatalf("note change: %v", err)
	}
	if err := service.NoteEvidenceChange(ctx, "t1", "kb1", "pkg-1"); err != nil {
		t.Fatalf("dedupe note: %v", err)
	}
	var count int64
	if err := db.Model(&models.ProjectionChange{}).Where("knowledge_base_id = ?", "kb1").Count(&count).Error; err != nil {
		t.Fatalf("count changes: %v", err)
	}
	if count != 1 {
		t.Fatalf("changes = %d, want 1", count)
	}
}

func TestCompleteGenerationReviewRejectionCarriesBaseVersion(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Model(&models.WikiPage{}).Where("id = ?", "wp1").Updates(map[string]any{
		"status": PagePublished, "current_version_id": "wv2", "published_version_id": "wv1", "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Updates(map[string]any{
		"review_status": ReviewApproved,
	}).Error; err != nil {
		t.Fatalf("approve base version: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv2", WikiPageID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 2, Content: "new",
		SourceHash: "h2", ReviewStatus: ReviewRejected, ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rejected version: %v", err)
	}
	if err := db.Create(&[]models.WikiGeneration{
		{ID: "gen-base", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 1, Status: status.Active, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gen-new", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 2, Status: status.GenerationReview, BaseGenerationID: stringPtr("gen-base"), TargetEpoch: 2, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed generations: %v", err)
	}
	if err := db.Create(&[]models.WikiGenerationPage{
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-base", WikiPageID: "wp1", WikiPageVersionID: stringPtr("wv1"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-new", WikiPageID: "wp1", WikiPageVersionID: stringPtr("wv2"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed generation pages: %v", err)
	}
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "kir-base", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1",
		Title: "Page", Content: "old", Status: IndexActive, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", ActiveWikiGenerationID: stringPtr("gen-base"),
		TargetEpoch: 2, WikiAppliedEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := service.completeGenerationReview(ctx, "t1", "kb1", "wv2"); err != nil {
		t.Fatalf("complete review: %v", err)
	}
	var generation models.WikiGeneration
	if err := db.First(&generation, "id = ?", "gen-new").Error; err != nil {
		t.Fatalf("load generation: %v", err)
	}
	if generation.Status != status.Active {
		t.Fatalf("generation = %s, want ACTIVE", generation.Status)
	}
	var mapping models.WikiGenerationPage
	if err := db.First(&mapping, "generation_id = ? AND wiki_page_id = ?", "gen-new", "wp1").Error; err != nil {
		t.Fatalf("load mapping: %v", err)
	}
	if mapping.Status != status.GenerationPageException || mapping.WikiPageVersionID == nil || *mapping.WikiPageVersionID != "wv1" {
		t.Fatalf("mapping = %+v", mapping)
	}
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.PublishedVersionID == nil || *page.PublishedVersionID != "wv1" {
		t.Fatalf("published version = %v, want wv1", page.PublishedVersionID)
	}
}

func TestActivateGenerationStaleAndRollbackValidation(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", TargetEpoch: 2, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := db.Create(&models.WikiGeneration{
		ID: "gen-stale", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 1, Status: status.GenerationReady,
		TargetEpoch: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed generation: %v", err)
	}
	if activated, err := service.activateGeneration(ctx, "t1", "kb1", "gen-stale"); err != nil || activated {
		t.Fatalf("stale activation = %v, %v", activated, err)
	}
	var stale models.WikiGeneration
	if err := db.First(&stale, "id = ?", "gen-stale").Error; err != nil {
		t.Fatalf("load generation: %v", err)
	}
	if stale.Status != status.Superseded {
		t.Fatalf("stale generation = %s, want SUPERSEDED", stale.Status)
	}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.RollbackGeneration(ctx, principal, "kb1", "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing rollback = %v, want not found", err)
	}
	if err := db.Create(&models.WikiGeneration{
		ID: "gen-building", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 2, Status: status.Building,
		TargetEpoch: 2, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed building generation: %v", err)
	}
	if _, err := service.RollbackGeneration(ctx, principal, "kb1", "gen-building"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("building rollback = %v, want conflict", err)
	}
}

func TestStatusExposesProjectionEpochs(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-1", IdempotencyKey: "package:pkg-1",
	}); err != nil {
		t.Fatalf("record change: %v", err)
	}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	status, err := service.Status(ctx, principal, "kb1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.TargetEpoch != 1 || status.PendingChanges != 1 {
		t.Fatalf("status = %+v", status)
	}
	if status.Invalidated {
		t.Fatalf("add change must not invalidate")
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "knowledge_base_id = ?", "kb1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.TargetEpoch != status.TargetEpoch || status.WikiAppliedEpoch != 0 {
		t.Fatalf("state/status mismatch: %+v / %+v", state, status)
	}
}

func TestCompleteGenerationReviewApprovalActivates(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Model(&models.WikiPage{}).Where("id = ?", "wp1").Updates(map[string]any{
		"status": PagePublished, "current_version_id": "wv2", "published_version_id": "wv1", "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := db.Model(&models.WikiPageVersion{}).Where("id = ?", "wv1").Updates(map[string]any{
		"review_status": ReviewApproved,
	}).Error; err != nil {
		t.Fatalf("approve base: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv2", WikiPageID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 2, Content: "new",
		SourceHash: "h2", ReviewStatus: ReviewApproved, ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed approved version: %v", err)
	}
	if err := db.Create(&[]models.WikiGeneration{
		{ID: "gen-base", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 1, Status: status.Active, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gen-new", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 2, Status: status.GenerationReview, BaseGenerationID: stringPtr("gen-base"), TargetEpoch: 2, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed generations: %v", err)
	}
	if err := db.Create(&[]models.WikiGenerationPage{
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-base", WikiPageID: "wp1", WikiPageVersionID: stringPtr("wv1"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-new", WikiPageID: "wp1", WikiPageVersionID: stringPtr("wv2"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed mappings: %v", err)
	}
	if err := db.Create(&[]models.KnowledgeIndexRecord{
		{ID: "kir1", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1", Title: "Page", Content: "old", Status: IndexActive, CreatedAt: now, UpdatedAt: now},
		{ID: "kir2", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv2", Title: "Page", Content: "new", Status: IndexActive, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", ActiveWikiGenerationID: stringPtr("gen-base"),
		TargetEpoch: 2, WikiAppliedEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := service.completeGenerationReview(ctx, "t1", "kb1", "wv2"); err != nil {
		t.Fatalf("complete review: %v", err)
	}
	var generation models.WikiGeneration
	if err := db.First(&generation, "id = ?", "gen-new").Error; err != nil {
		t.Fatalf("load generation: %v", err)
	}
	if generation.Status != status.Active {
		t.Fatalf("generation = %s, want ACTIVE", generation.Status)
	}
	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.PublishedVersionID == nil || *page.PublishedVersionID != "wv2" {
		t.Fatalf("published version = %v, want wv2", page.PublishedVersionID)
	}
}

func TestInvalidateDocumentRequiresIdentity(t *testing.T) {
	service, _ := newTriggerFixture(t)
	if err := service.InvalidateDocument(context.Background(), "", "", "", "", ""); err == nil {
		t.Fatalf("empty document invalidation must fail")
	}
}

func TestGenerationReviewAndRollbackEdgeCases(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := service.completeGenerationReview(ctx, "t1", "kb1", "missing-version"); err != nil {
		t.Fatalf("unknown version must be ignored: %v", err)
	}
	if err := db.Create(&models.WikiGeneration{
		ID: "gen-building", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 3, Status: status.Building,
		TargetEpoch: 3, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed building generation: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv-pending", WikiPageID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 3, Content: "pending",
		SourceHash: "h3", ReviewStatus: ReviewPending, ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pending version: %v", err)
	}
	if err := db.Create(&models.WikiGenerationPage{
		TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-building", WikiPageID: "wp1",
		WikiPageVersionID: stringPtr("wv-pending"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	if err := service.completeGenerationReview(ctx, "t1", "kb1", "wv-pending"); err != nil {
		t.Fatalf("building generation review must be ignored: %v", err)
	}
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", ActiveWikiGenerationID: stringPtr("gen-building"), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	generation, err := service.RollbackGeneration(ctx, principal, "kb1", "gen-building")
	if err != nil {
		t.Fatalf("active generation rollback must be a no-op: %v", err)
	}
	if generation.ID != "gen-building" {
		t.Fatalf("generation = %s", generation.ID)
	}
}

func TestActivateGenerationSkipsMissingMappings(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", ActiveWikiGenerationID: stringPtr("gen-old"), TargetEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := db.Create(&[]models.WikiGeneration{
		{ID: "gen-old", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 1, Status: status.Active, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gen-skip", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 2, Status: status.GenerationReady, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed generations: %v", err)
	}
	if err := db.Create(&[]models.WikiGenerationPage{
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-skip", WikiPageID: "wp-missing", WikiPageVersionID: stringPtr("wv-missing"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now},
		{TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-skip", WikiPageID: "wp-empty", Status: status.GenerationPageCarried, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed mappings: %v", err)
	}
	activated, err := service.activateGeneration(ctx, "t1", "kb1", "gen-skip")
	if err != nil || !activated {
		t.Fatalf("activate = %v, %v", activated, err)
	}
	var generation models.WikiGeneration
	if err := db.First(&generation, "id = ?", "gen-skip").Error; err != nil {
		t.Fatalf("load generation: %v", err)
	}
	if generation.Status != status.Active {
		t.Fatalf("generation = %s", generation.Status)
	}
}

func TestRollbackGenerationRejectsInvalidRequests(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	apiKey := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "api_key"}
	if _, err := service.RollbackGeneration(ctx, apiKey, "kb1", "gen-any"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key rollback = %v, want forbidden", err)
	}
	if err := db.Create(&[]models.WikiGeneration{
		{ID: "gen-current", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 1, Status: status.Active, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gen-empty", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 2, Status: status.Superseded, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "gen-pending", TenantID: "t1", KnowledgeBaseID: "kb1", GenerationNo: 3, Status: status.GenerationReady, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed generations: %v", err)
	}
	if err := db.Create(&models.WikiGenerationPage{
		TenantID: "t1", KnowledgeBaseID: "kb1", GenerationID: "gen-pending", WikiPageID: "wp1",
		WikiPageVersionID: stringPtr("wv-pending"), Status: status.GenerationPageGenerated, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pending mapping: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv-pending", WikiPageID: "wp1", TenantID: "t1", KnowledgeBaseID: "kb1", Version: 5, Content: "pending",
		SourceHash: "h5", ReviewStatus: ReviewPending, ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pending version: %v", err)
	}
	if err := db.Where("tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Delete(&models.KnowledgeProjectionState{}).Error; err != nil {
		t.Fatalf("clear state: %v", err)
	}
	if err := db.Create(&models.KnowledgeProjectionState{
		TenantID: "t1", KnowledgeBaseID: "kb1", ActiveWikiGenerationID: stringPtr("gen-current"), TargetEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.RollbackGeneration(ctx, principal, "kb1", "gen-empty"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("empty rollback = %v, want conflict", err)
	}
	if _, err := service.RollbackGeneration(ctx, principal, "kb1", "gen-pending"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("unapproved rollback = %v, want conflict", err)
	}
}

func TestBuildGenerationEmptyKnowledgeBase(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-empty", TenantID: "t1", OwnerID: "u1", Name: "Empty", ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	run := models.ProjectionRun{
		ID: "run-empty", TenantID: "t1", KnowledgeBaseID: "kb-empty", ProjectionType: ProjectionTypeWiki,
		Status: ProjectionRunning, TargetEpoch: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if activated, err := service.buildGeneration(ctx, &run, models.KnowledgeBase{ID: "kb-empty", TenantID: "t1", ACLVersion: 1}, "hash"); err != nil || activated {
		t.Fatalf("empty generation = %v, %v", activated, err)
	}
}

func TestSchedulePendingChangesHonorsMaxWait(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	service.batchQuiet = time.Hour
	service.batchMaxWait = 10 * time.Minute
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-1", IdempotencyKey: "package:pkg-1",
	}); err != nil {
		t.Fatalf("record change: %v", err)
	}
	if err := db.Model(&models.KnowledgeProjectionState{}).Where("knowledge_base_id = ?", "kb1").
		Update("last_change_at", time.Now().Add(-20*time.Minute)).Error; err != nil {
		t.Fatalf("age change: %v", err)
	}
	started, err := service.SchedulePendingChanges(ctx, 10)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if started != 2 {
		t.Fatalf("scheduled %d runs, want 2 despite quiet window", started)
	}
}

func TestServiceEdgeErrors(t *testing.T) {
	service, _ := newTriggerFixture(t)
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.ListGraphSnapshots(ctx, principal, "kb1", 10); err == nil {
		t.Fatalf("snapshots without a store must fail")
	}
	if purged, err := service.PurgeGenerationCache(ctx, 10); err != nil || purged != 0 {
		t.Fatalf("purge without cache = %d, %v", purged, err)
	}
	if _, err := service.Status(ctx, principal, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing status = %v", err)
	}
	if err := service.InvalidateKnowledgeBase(ctx, "t1", "missing", "test"); err != nil {
		t.Fatalf("missing invalidation must be a no-op: %v", err)
	}
	if _, err := service.JoinStatus(ctx, "t1", "missing"); err == nil {
		t.Fatalf("missing join must fail")
	}
}

type purgeCache struct{ calls int }

func (p *purgeCache) Get(context.Context, projectioncache.Key) ([]byte, bool, error) {
	return nil, false, nil
}

func (p *purgeCache) Put(context.Context, projectioncache.Key, []byte) error { return nil }

func (p *purgeCache) DeleteScope(context.Context, string, string, string) error { return nil }

func (p *purgeCache) PurgeOrphans(context.Context, int) (int64, error) {
	p.calls++
	return 4, nil
}

func TestServiceCacheAndRebuildEdges(t *testing.T) {
	service, db := newTriggerFixture(t)
	ctx := context.Background()
	cache := &purgeCache{}
	service.cache = cache
	if purged, err := service.PurgeGenerationCache(ctx, 10); err != nil || purged != 4 || cache.calls != 1 {
		t.Fatalf("purge = %d calls=%d err=%v", purged, cache.calls, err)
	}
	apiKey := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "api_key"}
	if _, err := service.Rebuild(ctx, apiKey, "kb1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key rebuild = %v", err)
	}
	if _, err := service.RebuildGraph(ctx, apiKey, "kb1"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("api key graph rebuild = %v", err)
	}
	session := authz.Principal{SubjectID: "u1", TenantID: "t1", CredentialType: "session"}
	if _, err := service.Rebuild(ctx, session, "missing"); err == nil {
		t.Fatalf("missing wiki rebuild must fail")
	}
	if _, err := service.RebuildGraph(ctx, session, "missing"); err == nil {
		t.Fatalf("missing graph rebuild must fail")
	}
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").
		Update("indexing_strategy", `{"wiki_enabled":false,"graph_enabled":false}`).Error; err != nil {
		t.Fatalf("disable projections: %v", err)
	}
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		EvidencePackageID: "pkg-1", IdempotencyKey: "package:pkg-1",
	}); err != nil {
		t.Fatalf("record change: %v", err)
	}
	if err := db.Model(&models.KnowledgeProjectionState{}).Where("knowledge_base_id = ?", "kb1").
		Update("last_change_at", time.Now().Add(-2*time.Minute)).Error; err != nil {
		t.Fatalf("age change: %v", err)
	}
	if started, err := service.SchedulePendingChanges(ctx, 10); err != nil || started != 0 {
		t.Fatalf("disabled projections scheduled %d, %v", started, err)
	}
}

func stringPtr(value string) *string { return &value }
