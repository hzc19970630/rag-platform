package tenant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantquota"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func ptr(value int64) *int64 { return &value }

func TestOverviewReportsOnlyThePrincipalTenant(t *testing.T) {
	service, _ := newTenantTestService(t)
	principal := authz.Principal{SubjectID: "owner-a", TenantID: "tenant-a", CredentialType: "session"}

	overview, err := service.Overview(context.Background(), principal)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.ID != "tenant-a" || overview.Name != "Tenant A" {
		t.Fatalf("overview identity = %+v", overview)
	}
	if overview.Members != (MemberCounts{Total: 4, Owners: 1, Admins: 1, Members: 1, Viewers: 1}) {
		t.Fatalf("member counts = %+v", overview.Members)
	}
	if overview.KnowledgeBases != 1 || overview.Documents != 1 || overview.StorageBytes != 100 {
		t.Fatalf("content counts = kb %d, docs %d, bytes %d", overview.KnowledgeBases, overview.Documents, overview.StorageBytes)
	}
	if overview.ChatQuestionsToday != 2 {
		t.Fatalf("today's user questions = %d, want 2", overview.ChatQuestionsToday)
	}

	other, err := service.Overview(context.Background(), authz.Principal{SubjectID: "owner-b", TenantID: "tenant-b"})
	if err != nil {
		t.Fatalf("tenant-b overview: %v", err)
	}
	if other.KnowledgeBases != 1 || other.Documents != 1 || other.StorageBytes != 200 || other.ChatQuestionsToday != 1 {
		t.Fatalf("tenant-b must not see tenant-a usage: %+v", other)
	}
}

func TestOverviewRejectsNonOperators(t *testing.T) {
	service, _ := newTenantTestService(t)
	_, err := service.Overview(context.Background(), authz.Principal{SubjectID: "viewer-a", TenantID: "tenant-a"})
	if err != apperror.ErrForbidden {
		t.Fatalf("viewer overview error = %v, want forbidden", err)
	}
	_, err = service.Quota(context.Background(), authz.Principal{SubjectID: "member-a", TenantID: "tenant-a"})
	if err != apperror.ErrForbidden {
		t.Fatalf("member quota error = %v, want forbidden", err)
	}
	_, err = service.UpdateQuota(context.Background(), authz.Principal{SubjectID: "owner-a", TenantID: "tenant-a", CredentialType: "api_key"}, tenantquota.Overrides{})
	if err != apperror.ErrForbidden {
		t.Fatalf("api-key quota update error = %v, want forbidden", err)
	}
}

func TestQuotaResolvesOverridesAndUpdateReplacesTheObject(t *testing.T) {
	service, db := newTenantTestService(t)
	principal := authz.Principal{SubjectID: "admin-a", TenantID: "tenant-a", CredentialType: "session", RequestID: "req-quota"}

	effective, err := service.Quota(context.Background(), principal)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if effective.MaxDocuments.Value != 10 || effective.MaxDocuments.Overridden {
		t.Fatalf("initial document quota = %+v, want deployment default 10", effective.MaxDocuments)
	}
	if effective.ChatRequestsPerDay.Value != 0 || !effective.ChatRequestsPerDay.Overridden {
		t.Fatalf("initial chat quota = %+v, want stored unlimited override", effective.ChatRequestsPerDay)
	}

	updated, err := service.UpdateQuota(context.Background(), principal, tenantquota.Overrides{MaxStorageBytes: ptr(2000)})
	if err != nil {
		t.Fatalf("update quota: %v", err)
	}
	if updated.MaxDocuments.Value != 10 || updated.MaxDocuments.Overridden {
		t.Fatalf("replaced document quota = %+v, want inheritance", updated.MaxDocuments)
	}
	if updated.MaxStorageBytes.Value != 2000 || !updated.MaxStorageBytes.Overridden {
		t.Fatalf("storage quota = %+v, want tenant override 2000", updated.MaxStorageBytes)
	}
	if updated.ChatRequestsPerDay.Value != 5 || updated.ChatRequestsPerDay.Overridden {
		t.Fatalf("chat quota = %+v, want inherited default 5", updated.ChatRequestsPerDay)
	}

	var audit models.AuditLog
	if err := db.Where("action = ? AND resource_id = ?", "update_tenant_quota", "tenant-a").First(&audit).Error; err != nil {
		t.Fatalf("quota audit row: %v", err)
	}
	if audit.ActorID != "admin-a" || audit.RequestID != "req-quota" || !strings.Contains(audit.Reason, "max_storage_bytes") {
		t.Fatalf("audit row = %+v", audit)
	}

	var tenantB models.Tenant
	if err := db.First(&tenantB, "id = ?", "tenant-b").Error; err != nil {
		t.Fatalf("load tenant-b: %v", err)
	}
	if tenantB.Quota != "{}" {
		t.Fatalf("tenant-a quota update changed tenant-b: %s", tenantB.Quota)
	}

	_, err = service.UpdateQuota(context.Background(), principal, tenantquota.Overrides{MaxDocuments: ptr(-1)})
	if _, ok := apperror.AsValidation(err); !ok {
		t.Fatalf("negative quota error = %v, want validation error", err)
	}
}

func newTenantTestService(t *testing.T) (*Service, *gorm.DB) {
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
	if err := db.AutoMigrate(
		&models.Tenant{}, &models.User{}, &models.Membership{}, &models.AuthorizationRevision{},
		&models.KnowledgeBase{}, &models.Document{}, &models.DocumentVersion{},
		&models.Message{}, &models.AuditLog{},
	); err != nil {
		t.Fatalf("migrate tenant tables: %v", err)
	}

	now := time.Now()
	if err := db.Create(&[]models.Tenant{
		{ID: "tenant-a", Name: "Tenant A", Quota: `{"chat_requests_per_day":0}`, CreatedAt: now, UpdatedAt: now},
		{ID: "tenant-b", Name: "Tenant B", Quota: "{}", CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	if err := db.Create(&[]models.User{
		{ID: "owner-a", Email: "owner-a@example.com", DisplayName: "Owner A"},
		{ID: "admin-a", Email: "admin-a@example.com", DisplayName: "Admin A"},
		{ID: "member-a", Email: "member-a@example.com", DisplayName: "Member A"},
		{ID: "viewer-a", Email: "viewer-a@example.com", DisplayName: "Viewer A"},
		{ID: "owner-b", Email: "owner-b@example.com", DisplayName: "Owner B"},
	}).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner"},
		{TenantID: "tenant-a", UserID: "admin-a", Role: "admin"},
		{TenantID: "tenant-a", UserID: "member-a", Role: "member"},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer"},
		{TenantID: "tenant-b", UserID: "owner-b", Role: "owner"},
	}).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := db.Create(&[]models.KnowledgeBase{
		{ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB A"},
		{ID: "kb-b", TenantID: "tenant-b", OwnerID: "owner-b", Name: "KB B"},
	}).Error; err != nil {
		t.Fatalf("seed knowledge bases: %v", err)
	}
	deletedAt := now
	if err := db.Create(&[]models.Document{
		{ID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "owner-a", Filename: "a.pdf", MimeType: "application/pdf", Status: "READY"},
		{ID: "doc-a-deleted", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "owner-a", Filename: "old.pdf", MimeType: "application/pdf", Status: "DELETED", DeletedAt: &deletedAt},
		{ID: "doc-b", TenantID: "tenant-b", KnowledgeBaseID: "kb-b", OwnerID: "owner-b", Filename: "b.pdf", MimeType: "application/pdf", Status: "READY"},
	}).Error; err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	if err := db.Create(&[]models.DocumentVersion{
		{ID: "ver-a", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Version: 1, SizeBytes: 100, Status: "READY"},
		{ID: "ver-a-deleted", DocumentID: "doc-a-deleted", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Version: 1, SizeBytes: 500, Status: "READY"},
		{ID: "ver-b", DocumentID: "doc-b", TenantID: "tenant-b", KnowledgeBaseID: "kb-b", Version: 1, SizeBytes: 200, Status: "READY"},
	}).Error; err != nil {
		t.Fatalf("seed document versions: %v", err)
	}
	if err := db.Create(&[]models.Message{
		{ID: "msg-a-1", ConversationID: "conv-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "owner-a", RequestID: "r1", Role: "user", Status: "COMPLETED", CreatedAt: now},
		{ID: "msg-a-2", ConversationID: "conv-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "owner-a", RequestID: "r2", Role: "user", Status: "COMPLETED", CreatedAt: now},
		{ID: "msg-a-3", ConversationID: "conv-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "owner-a", RequestID: "r1", Role: "assistant", Status: "COMPLETED", CreatedAt: now},
		{ID: "msg-b-1", ConversationID: "conv-b", TenantID: "tenant-b", KnowledgeBaseID: "kb-b", UserID: "owner-b", RequestID: "r3", Role: "user", Status: "COMPLETED", CreatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	resolver := tenantquota.NewResolver(db, tenantquota.Limits{MaxDocuments: 10, MaxStorageBytes: 1000, ChatRequestsPerDay: 5})
	return NewService(db, authzService, resolver), db
}
