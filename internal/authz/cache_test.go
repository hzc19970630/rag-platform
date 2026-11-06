package authz

import (
	"context"
	"testing"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openAuthzDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Membership{}, &models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.AuthorizationRevision{}, &models.AuditLog{}, &models.AuthSession{}); err != nil {
		t.Fatalf("migrate authz tables: %v", err)
	}
	return db
}

func seedAuthz(t *testing.T, db *gorm.DB) {
	t.Helper()
	memberships := []models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner"},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer"},
		{TenantID: "tenant-b", UserID: "owner-b", Role: "owner"},
	}
	if err := db.Create(&memberships).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	knowledgeBases := []models.KnowledgeBase{
		{ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB A"},
		{ID: "kb-b", TenantID: "tenant-b", OwnerID: "owner-b", Name: "KB B"},
	}
	if err := db.Create(&knowledgeBases).Error; err != nil {
		t.Fatalf("seed knowledge bases: %v", err)
	}
	if err := db.Create(&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-a", UserID: "viewer-a", Role: "viewer"}).Error; err != nil {
		t.Fatalf("seed knowledge base member: %v", err)
	}
}

func TestTenantPolicyScopesAndRevocationIsImmediate(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	ctx := context.Background()

	owner := Principal{SubjectID: "owner-a", TenantID: "tenant-a"}
	viewer := Principal{SubjectID: "viewer-a", TenantID: "tenant-a"}

	scope, err := service.BuildScope(ctx, owner, QueryTarget{KnowledgeBaseID: "kb-a"})
	if err != nil {
		t.Fatalf("owner scope: %v", err)
	}
	if len(scope.KnowledgeBaseIDs) != 1 || scope.KnowledgeBaseIDs[0] != "kb-a" {
		t.Fatalf("owner scope = %+v", scope)
	}

	if _, err := service.BuildScope(ctx, Principal{SubjectID: "owner-b", TenantID: "tenant-b"}, QueryTarget{KnowledgeBaseID: "kb-a"}); err == nil {
		t.Fatalf("cross-tenant scope must be forbidden")
	}

	if allowed, err := service.Authorize(ctx, owner, "write", "/knowledge-bases/kb-a"); err != nil || !allowed {
		t.Fatalf("owner write = %v, %v", allowed, err)
	}
	if allowed, err := service.Authorize(ctx, viewer, "read", "/knowledge-bases/kb-a"); err != nil || !allowed {
		t.Fatalf("viewer read = %v, %v", allowed, err)
	}
	if allowed, err := service.Authorize(ctx, viewer, "write", "/knowledge-bases/kb-a"); err != nil || allowed {
		t.Fatalf("viewer write = %v, %v", allowed, err)
	}

	// Revoke the knowledge-base grant. The next check must observe it without a
	// cache TTL: the revision read is fresh and the scope key includes it.
	if err := db.Where("knowledge_base_id = ? AND user_id = ?", "kb-a", "viewer-a").Delete(&models.KnowledgeBaseMember{}).Error; err != nil {
		t.Fatalf("revoke member: %v", err)
	}
	if err := service.BumpRevision(ctx, "tenant-a"); err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	if _, err := service.BuildScope(ctx, viewer, QueryTarget{KnowledgeBaseID: "kb-a"}); err == nil {
		t.Fatalf("revoked viewer scope must be forbidden")
	}
	if allowed, err := service.Authorize(ctx, viewer, "read", "/knowledge-bases/kb-a"); err != nil || allowed {
		t.Fatalf("revoked viewer read = %v, %v", allowed, err)
	}
}

func TestBumpRevisionOnlyInvalidatesOneTenant(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	ctx := context.Background()
	if _, err := service.BuildScope(ctx, Principal{SubjectID: "owner-a", TenantID: "tenant-a"}, QueryTarget{}); err != nil {
		t.Fatalf("tenant a scope: %v", err)
	}
	if _, err := service.BuildScope(ctx, Principal{SubjectID: "owner-b", TenantID: "tenant-b"}, QueryTarget{}); err != nil {
		t.Fatalf("tenant b scope: %v", err)
	}
	service.mu.RLock()
	before := len(service.tenants)
	service.mu.RUnlock()
	if before != 2 {
		t.Fatalf("tenant policies = %d, want 2", before)
	}

	if err := service.BumpRevision(ctx, "tenant-a"); err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	service.mu.RLock()
	_, hasA := service.tenants["tenant-a"]
	_, hasB := service.tenants["tenant-b"]
	service.mu.RUnlock()
	if hasA {
		t.Fatalf("tenant-a policy must be invalidated")
	}
	if !hasB {
		t.Fatalf("tenant-b policy must survive a tenant-a change")
	}
}

func TestScopeCacheReturnsIndependentCopy(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	service, err := NewService(db)
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	principal := Principal{SubjectID: "owner-a", TenantID: "tenant-a"}
	first, err := service.BuildScope(context.Background(), principal, QueryTarget{})
	if err != nil {
		t.Fatalf("first scope: %v", err)
	}
	first.KnowledgeBaseIDs[0] = "mutated"
	second, err := service.BuildScope(context.Background(), principal, QueryTarget{})
	if err != nil {
		t.Fatalf("second scope: %v", err)
	}
	if second.KnowledgeBaseIDs[0] != "kb-a" {
		t.Fatalf("cached scope was mutated through the returned slice: %+v", second)
	}
}
