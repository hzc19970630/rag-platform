package knowledgebase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestEmbeddingBindingIsAdoptedAndThenLockedByDocuments(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}

	now := time.Now().UTC()
	document := models.Document{
		ID: "doc-1", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", OwnerID: "owner",
		Filename: "note.md", MimeType: "text/markdown", Status: "EVIDENCE_READY",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&document).Error; err != nil {
		t.Fatalf("create document: %v", err)
	}

	// A pre-upgrade row has no binding; adopting the deployment binding is a
	// backfill, not a change, so documents do not block it.
	original := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	model, dimension := "m-1024", 1024
	row, err := original.Update(ctx, principal, "kb-1", UpdateInput{EmbeddingModel: &model, EmbeddingDimension: &dimension})
	if err != nil {
		t.Fatalf("adopt deployment binding: %v", err)
	}
	if row.EmbeddingModel != model || row.EmbeddingDimension != dimension {
		t.Fatalf("unexpected binding after adoption: %s/%d", row.EmbeddingModel, row.EmbeddingDimension)
	}

	// After a deployment switch the knowledge base is still pinned to the old
	// model, and the API must not silently move it.
	authorizer := mustAuthorizer(t, db)
	switched := NewService(db, authorizer, EmbeddingBinding{Model: "m-768", Dimension: 768})
	nextModel, nextDimension := "m-768", 768
	if _, err := switched.Update(ctx, principal, "kb-1", UpdateInput{EmbeddingModel: &nextModel, EmbeddingDimension: &nextDimension}); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("expected locked binding error, got %v", err)
	}

	// Without documents the same rebind is allowed.
	empty := models.KnowledgeBase{
		ID: "kb-empty", TenantID: "tenant-1", OwnerID: "owner", Name: "Empty",
		RetrievalConfig: "{}", IndexingStrategy: "{}", ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&empty).Error; err != nil {
		t.Fatalf("create empty knowledge base: %v", err)
	}
	if err := db.Create(&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-empty", UserID: "editor", Role: "editor", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("grant editor access: %v", err)
	}
	if err := authorizer.RefreshPolicies(ctx); err != nil {
		t.Fatalf("refresh policies: %v", err)
	}
	if _, err := switched.Update(ctx, principal, "kb-empty", UpdateInput{EmbeddingModel: &nextModel, EmbeddingDimension: &nextDimension}); err != nil {
		t.Fatalf("rebind empty knowledge base: %v", err)
	}

	// A binding this deployment cannot serve is rejected before the lock check.
	badModel, badDimension := "m-unknown", 99
	if _, err := switched.Update(ctx, principal, "kb-empty", UpdateInput{EmbeddingModel: &badModel, EmbeddingDimension: &badDimension}); err == nil || !strings.Contains(err.Error(), "unsupported embedding binding") {
		t.Fatalf("expected unsupported binding error, got %v", err)
	}
}

func TestBackfillEmbeddingBindingNeverOverwritesExistingBinding(t *testing.T) {
	db := openConfigurationTestDB(t)
	ctx := context.Background()

	updated, err := BackfillEmbeddingBinding(ctx, db, EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected one pre-upgrade knowledge base, got %d", updated)
	}
	var row models.KnowledgeBase
	if err := db.Where("id = ?", "kb-1").First(&row).Error; err != nil {
		t.Fatalf("load knowledge base: %v", err)
	}
	if row.EmbeddingModel != "m-1024" || row.EmbeddingDimension != 1024 {
		t.Fatalf("unexpected binding after backfill: %s/%d", row.EmbeddingModel, row.EmbeddingDimension)
	}

	updated, err = BackfillEmbeddingBinding(ctx, db, EmbeddingBinding{Model: "m-768", Dimension: 768})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if updated != 0 {
		t.Fatalf("backfill must not rewrite an existing binding, updated %d", updated)
	}
}
