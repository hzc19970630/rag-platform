package knowledgebase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type fakeModelCatalog struct {
	capability map[string]model.Capability
	refs       map[string]model.ModelRef
}

func (f fakeModelCatalog) ResolveBinding(capability model.Capability, requested string) (model.ModelRef, error) {
	ref, ok := f.refs[requested]
	if !ok || f.capability[requested] != capability {
		return model.ModelRef{}, apperror.Invalidf("unsupported %s model %q", capability, requested)
	}
	return ref, nil
}

func newFakeModelCatalog() fakeModelCatalog {
	return fakeModelCatalog{
		capability: map[string]model.Capability{
			"chat-a": model.CapabilityChat, "chat-b": model.CapabilityChat, "embed-x": model.CapabilityEmbedding,
		},
		refs: map[string]model.ModelRef{
			"chat-a":  {Model: "chat-a", Provider: "remote", Version: "2024-08"},
			"chat-b":  {Model: "chat-b", Provider: "remote", Version: "2025-01"},
			"embed-x": {Model: "embed-x", Provider: "remote", Version: "v3"},
		},
	}
}

func bindModel(t *testing.T, service *Service, principal authz.Principal, id string, input UpdateInput) models.KnowledgeBase {
	t.Helper()
	row, err := service.Update(context.Background(), principal, id, input)
	if err != nil {
		t.Fatalf("update knowledge base %s: %v", id, err)
	}
	return row
}

func chatBinding(name string) UpdateInput { return UpdateInput{ChatModel: &name} }

func TestKnowledgeBasesBindDifferentChatModelsAndAudit(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	now := time.Now().UTC()
	second := models.KnowledgeBase{
		ID: "kb-2", TenantID: "tenant-1", OwnerID: "owner", Name: "Second",
		RetrievalConfig: "{}", IndexingStrategy: "{}", ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second knowledge base: %v", err)
	}
	if err := db.Create(&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-2", UserID: "editor", Role: "editor", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("grant editor access: %v", err)
	}
	authorizer := mustAuthorizer(t, db)
	if err := authorizer.RefreshPolicies(context.Background()); err != nil {
		t.Fatalf("refresh policies: %v", err)
	}
	service := NewService(db, authorizer, EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	service.SetModelCatalog(newFakeModelCatalog())
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}

	first := bindModel(t, service, principal, "kb-1", chatBinding("chat-a"))
	secondRow := bindModel(t, service, principal, "kb-2", chatBinding("chat-b"))
	if first.ChatModel != "chat-a" || first.ChatModelVersion != "2024-08" {
		t.Fatalf("unexpected kb-1 binding: %s/%s", first.ChatModel, first.ChatModelVersion)
	}
	if secondRow.ChatModel != "chat-b" || secondRow.ChatModelVersion != "2025-01" {
		t.Fatalf("unexpected kb-2 binding: %s/%s", secondRow.ChatModel, secondRow.ChatModelVersion)
	}

	var audits []models.AuditLog
	if err := db.Where("resource_type = ?", "model_binding").Order("created_at").Find(&audits).Error; err != nil {
		t.Fatalf("load binding audits: %v", err)
	}
	if len(audits) != 2 {
		t.Fatalf("expected two binding audits, got %d", len(audits))
	}
	firstAudit := decodeBindingAudit(t, audits[0].Reason)
	if firstAudit["capability"] != "chat" || firstAudit["model"] != "chat-a" || firstAudit["version"] != "2024-08" || firstAudit["previousModel"] != "" {
		t.Fatalf("unexpected kb-1 audit: %+v", firstAudit)
	}
	secondAudit := decodeBindingAudit(t, audits[1].Reason)
	if secondAudit["model"] != "chat-b" || secondAudit["previousVersion"] != "" {
		t.Fatalf("unexpected kb-2 audit: %+v", secondAudit)
	}
}

func TestModelBindingRejectsUnsupportedModelAndWritesNoAudit(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	service.SetModelCatalog(newFakeModelCatalog())
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}

	unknown := "chat-retired"
	if _, err := service.Update(context.Background(), principal, "kb-1", chatBinding(unknown)); err == nil {
		t.Fatal("expected unsupported model to be rejected")
	} else if _, ok := apperror.AsValidation(err); !ok {
		t.Fatalf("expected validation error, got %v", err)
	}
	var row models.KnowledgeBase
	if err := db.Where("id = ?", "kb-1").First(&row).Error; err != nil {
		t.Fatalf("load knowledge base: %v", err)
	}
	if row.ChatModel != "" {
		t.Fatalf("failed update must not persist a binding, got %q", row.ChatModel)
	}
	var audits int64
	if err := db.Model(&models.AuditLog{}).Where("resource_type = ?", "model_binding").Count(&audits).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 0 {
		t.Fatalf("failed update must not write an audit, got %d", audits)
	}

	// A capability mismatch is rejected the same way, not silently accepted.
	embeddingName := "embed-x"
	if _, err := service.Update(context.Background(), principal, "kb-1", chatBinding(embeddingName)); err == nil {
		t.Fatal("expected wrong-capability model to be rejected")
	}
}

func TestModelBindingRequiresConfiguredCatalog(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}

	// Clearing an override without a catalog is a no-op and stays allowed.
	empty := ""
	if _, err := service.Update(context.Background(), principal, "kb-1", chatBinding(empty)); err != nil {
		t.Fatalf("clearing an unset binding: %v", err)
	}
	if _, err := service.Update(context.Background(), principal, "kb-1", chatBinding("chat-a")); err == nil || !strings.Contains(err.Error(), "model registry is not configured") {
		t.Fatalf("expected registry configuration error, got %v", err)
	}
}

func TestModelBindingClearRecordsPreviousVersion(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	service.SetModelCatalog(newFakeModelCatalog())
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}

	bindModel(t, service, principal, "kb-1", chatBinding("chat-a"))
	// Re-selecting the same binding is not a change and must not add an audit.
	bindModel(t, service, principal, "kb-1", chatBinding("chat-a"))

	cleared := bindModel(t, service, principal, "kb-1", chatBinding(""))
	if cleared.ChatModel != "" || cleared.ChatModelVersion != "" {
		t.Fatalf("clearing the override must reset both fields, got %q/%q", cleared.ChatModel, cleared.ChatModelVersion)
	}

	var audits []models.AuditLog
	if err := db.Where("resource_type = ?", "model_binding").Order("created_at").Find(&audits).Error; err != nil {
		t.Fatalf("load binding audits: %v", err)
	}
	if len(audits) != 2 {
		t.Fatalf("expected bind and clear audits, got %d", len(audits))
	}
	clearAudit := decodeBindingAudit(t, audits[1].Reason)
	if clearAudit["model"] != "" || clearAudit["previousModel"] != "chat-a" || clearAudit["previousVersion"] != "2024-08" {
		t.Fatalf("unexpected clear audit: %+v", clearAudit)
	}
}

func TestModelBindingDoesNotLoosenEmbeddingDimensionContract(t *testing.T) {
	db := openConfigurationTestDB(t)
	seedAuthorization(t, db)
	service := NewService(db, mustAuthorizer(t, db), EmbeddingBinding{Model: "m-1024", Dimension: 1024})
	service.SetModelCatalog(newFakeModelCatalog())
	principal := authz.Principal{SubjectID: "editor", TenantID: "tenant-1"}

	modelName, dimension := "m-1024", 768
	if _, err := service.Update(context.Background(), principal, "kb-1", UpdateInput{EmbeddingModel: &modelName, EmbeddingDimension: &dimension}); err == nil || !strings.Contains(err.Error(), "unsupported embedding binding") {
		t.Fatalf("expected dimension mismatch rejection, got %v", err)
	}
	otherModel, otherDimension := "embed-x", 1024
	if _, err := service.Update(context.Background(), principal, "kb-1", UpdateInput{EmbeddingModel: &otherModel, EmbeddingDimension: &otherDimension}); err == nil || !strings.Contains(err.Error(), "unsupported embedding binding") {
		t.Fatalf("models outside the deployment binding must stay rejected, got %v", err)
	}

	// A chat binding does not touch the embedding columns.
	var before models.KnowledgeBase
	if err := db.Where("id = ?", "kb-1").First(&before).Error; err != nil {
		t.Fatalf("load knowledge base: %v", err)
	}
	row := bindModel(t, service, principal, "kb-1", chatBinding("chat-a"))
	if row.EmbeddingModel != before.EmbeddingModel || row.EmbeddingDimension != before.EmbeddingDimension {
		t.Fatalf("chat binding must not change embedding binding, got %q/%d", row.EmbeddingModel, row.EmbeddingDimension)
	}
}

func decodeBindingAudit(t *testing.T, reason string) map[string]any {
	t.Helper()
	const prefix = "model_binding_updated: "
	if !strings.HasPrefix(reason, prefix) {
		t.Fatalf("unexpected audit reason: %q", reason)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(reason, prefix)), &payload); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	return payload
}
