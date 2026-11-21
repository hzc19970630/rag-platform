package knowledgeprojection

import (
	"context"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestInvalidateDocumentHidesPagesAndIndex(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb1").
		Update("indexing_strategy", `{"wiki_enabled":true,"graph_enabled":true}`).Error; err != nil {
		t.Fatalf("enable projections: %v", err)
	}
	if err := db.Model(&models.WikiPage{}).Where("id = ?", "wp1").Updates(map[string]any{
		"status": PagePublished, "published_version_id": "wv1", "updated_at": now,
	}).Error; err != nil {
		t.Fatalf("publish page: %v", err)
	}
	if err := db.Create(&models.KnowledgeIndexRecord{
		ID: "idx1", TenantID: "t1", KnowledgeBaseID: "kb1", WikiPageID: "wp1", WikiPageVersionID: "wv1",
		Title: "Page", Content: "content", Status: IndexActive, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		DocumentID: "d1", DocumentVersionID: "dv1", ContentHash: "hash",
	}); err != nil {
		t.Fatalf("seed add change: %v", err)
	}

	if err := service.InvalidateDocument(ctx, "t1", "kb1", "d1", "dv1", "document_deleted"); err != nil {
		t.Fatalf("invalidate document: %v", err)
	}

	var page models.WikiPage
	if err := db.First(&page, "id = ?", "wp1").Error; err != nil {
		t.Fatalf("load page: %v", err)
	}
	if page.Status != PageStale {
		t.Fatalf("page status = %s, want %s", page.Status, PageStale)
	}
	var indexed int64
	if err := db.Model(&models.KnowledgeIndexRecord{}).Where("wiki_page_id = ?", "wp1").Count(&indexed).Error; err != nil {
		t.Fatalf("count index: %v", err)
	}
	if indexed != 0 {
		t.Fatalf("knowledge index rows = %d, want 0", indexed)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.TargetEpoch != 2 || state.InvalidatedAt == nil {
		t.Fatalf("state epoch=%d invalidated=%v", state.TargetEpoch, state.InvalidatedAt)
	}
	status, err := service.JoinStatus(ctx, "t1", "kb1")
	if err != nil {
		t.Fatalf("join status: %v", err)
	}
	if status != JoinKnowledgePartial {
		t.Fatalf("join status = %s, want %s", status, JoinKnowledgePartial)
	}
	capability, err := service.CapabilityStatus(ctx, "t1", "kb1")
	if err != nil {
		t.Fatalf("capability status: %v", err)
	}
	if capability.WikiReady || capability.RelationReady {
		t.Fatalf("capabilities must be closed after delete: %+v", capability)
	}
}

func TestInvalidateKnowledgeBaseKeepsEpoch(t *testing.T) {
	service, db := newProjectionViewsFixture(t)
	ctx := context.Background()
	if _, err := service.RecordChange(ctx, ChangeInput{
		TenantID: "t1", KnowledgeBaseID: "kb1", Kind: ChangeAdd,
		DocumentID: "d1", DocumentVersionID: "dv1", ContentHash: "hash",
	}); err != nil {
		t.Fatalf("seed add change: %v", err)
	}
	if err := service.InvalidateKnowledgeBase(ctx, "t1", "kb1", "membership_changed"); err != nil {
		t.Fatalf("invalidate knowledge base: %v", err)
	}
	var state models.KnowledgeProjectionState
	if err := db.First(&state, "tenant_id = ? AND knowledge_base_id = ?", "t1", "kb1").Error; err != nil {
		t.Fatalf("state row must survive invalidation: %v", err)
	}
	if state.TargetEpoch != 2 {
		t.Fatalf("target epoch = %d, want 2", state.TargetEpoch)
	}
	if state.InvalidatedAt == nil {
		t.Fatalf("acl invalidation must set invalidated_at")
	}
	var changes []models.ProjectionChange
	if err := db.Order("epoch").Find(&changes).Error; err != nil {
		t.Fatalf("load changes: %v", err)
	}
	if len(changes) != 2 || changes[1].Kind != ChangeACL {
		t.Fatalf("changes = %+v", changes)
	}
}
