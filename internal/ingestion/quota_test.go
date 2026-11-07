package ingestion

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fixedQuotaResolver struct {
	limits tenantquota.Limits
	err    error
}

func (r fixedQuotaResolver) Limits(context.Context, string) (tenantquota.Limits, error) {
	return r.limits, r.err
}

func TestCheckQuotaUsesResolvedTenantLimits(t *testing.T) {
	db := openQuotaDB(t)
	if err := db.Create(&models.Document{
		ID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "user-a",
		Filename: "a.pdf", MimeType: "application/pdf", Status: "READY",
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-a", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		Version: 1, SizeBytes: 100, Status: "READY",
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}

	// The static deployment limit allows more documents than the tenant override.
	service := NewService(db, nil, nil, nil, 0, 30, Quota{MaxDocuments: 100})
	service.SetQuotaResolver(fixedQuotaResolver{limits: tenantquota.Limits{MaxDocuments: 1}})
	err := service.checkQuota(context.Background(), db, "tenant-a", 0)
	if !errors.Is(err, apperror.ErrQuotaExceeded) {
		t.Fatalf("document quota error = %v, want quota exceeded", err)
	}

	service.SetQuotaResolver(fixedQuotaResolver{limits: tenantquota.Limits{MaxStorageBytes: 50}})
	err = service.checkQuota(context.Background(), db, "tenant-a", 10)
	if !errors.Is(err, apperror.ErrQuotaExceeded) {
		t.Fatalf("storage quota error = %v, want quota exceeded", err)
	}

	// Zero at the tenant tier is an explicit unlimited, not a fallback to the
	// deployment ceiling.
	service.SetQuotaResolver(fixedQuotaResolver{limits: tenantquota.Limits{}})
	if err := service.checkQuota(context.Background(), db, "tenant-a", 0); err != nil {
		t.Fatalf("unlimited tenant quota = %v", err)
	}

	service.SetQuotaResolver(fixedQuotaResolver{err: errors.New("quota store down")})
	if err := service.checkQuota(context.Background(), db, "tenant-a", 0); err == nil {
		t.Fatal("a quota resolver failure must not silently allow the upload")
	}
}

func openQuotaDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&models.Document{}, &models.DocumentVersion{}); err != nil {
		t.Fatalf("migrate ingestion tables: %v", err)
	}
	return db
}
