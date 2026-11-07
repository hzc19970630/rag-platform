package tenantquota

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func ptr(value int64) *int64 { return &value }

func TestResolveKeepsDeploymentDefaultsAndTenantOverridesApart(t *testing.T) {
	defaults := Limits{MaxDocuments: 100, MaxStorageBytes: 1 << 30, ChatRequestsPerDay: 50}
	effective := Resolve(defaults, Overrides{
		MaxDocuments:       ptr(10),
		ChatRequestsPerDay: ptr(0),
	})
	if effective.MaxDocuments.Value != 10 || !effective.MaxDocuments.Overridden || effective.MaxDocuments.Default != 100 {
		t.Fatalf("document limit = %+v, want tenant override 10 over default 100", effective.MaxDocuments)
	}
	if effective.MaxStorageBytes.Value != 1<<30 || effective.MaxStorageBytes.Overridden {
		t.Fatalf("storage limit = %+v, want inherited deployment default", effective.MaxStorageBytes)
	}
	if effective.ChatRequestsPerDay.Value != 0 || !effective.ChatRequestsPerDay.Overridden {
		t.Fatalf("chat limit = %+v, want explicit unlimited override", effective.ChatRequestsPerDay)
	}
	if effective.Limits() != (Limits{MaxDocuments: 10, MaxStorageBytes: 1 << 30, ChatRequestsPerDay: 0}) {
		t.Fatalf("effective limits = %+v", effective.Limits())
	}
}

func TestOverridesRejectNegativeValues(t *testing.T) {
	overrides := Overrides{MaxStorageBytes: ptr(-1)}
	if err := overrides.Validate(); err == nil || !strings.Contains(err.Error(), "maxStorageBytes") {
		t.Fatalf("negative override error = %v", err)
	}
}

func TestParseRejectsMalformedAndNegativeStoredQuota(t *testing.T) {
	if _, err := Parse("{not json"); err == nil {
		t.Fatal("malformed stored quota must fail closed")
	}
	if _, err := Parse(`{"max_documents":-5}`); err == nil {
		t.Fatal("negative stored quota must fail closed")
	}
	overrides, err := Parse(`{}`)
	if err != nil || overrides.MaxDocuments != nil {
		t.Fatalf("empty quota = %+v, %v", overrides, err)
	}
}

func TestResolverRoundTripsOverrides(t *testing.T) {
	db := openQuotaTestDB(t)
	if err := db.Create(&models.Tenant{ID: "tenant-a", Name: "A", Quota: "{}"}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	resolver := NewResolver(db, Limits{MaxDocuments: 10, MaxStorageBytes: 1000, ChatRequestsPerDay: 5})
	effective, err := resolver.Effective(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("effective quota: %v", err)
	}
	if effective.MaxDocuments.Value != 10 || effective.MaxDocuments.Overridden {
		t.Fatalf("initial quota = %+v, want deployment default", effective.MaxDocuments)
	}

	if err := resolver.Save(context.Background(), "tenant-a", Overrides{MaxDocuments: ptr(3), ChatRequestsPerDay: ptr(0)}); err != nil {
		t.Fatalf("save quota: %v", err)
	}
	effective, err = resolver.Effective(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("effective quota after save: %v", err)
	}
	if effective.MaxDocuments.Value != 3 || !effective.MaxDocuments.Overridden {
		t.Fatalf("document limit = %+v, want tenant override", effective.MaxDocuments)
	}
	if effective.MaxStorageBytes.Value != 1000 || effective.MaxStorageBytes.Overridden {
		t.Fatalf("storage limit = %+v, want deployment default", effective.MaxStorageBytes)
	}
	if effective.ChatRequestsPerDay.Value != 0 || !effective.ChatRequestsPerDay.Overridden {
		t.Fatalf("chat limit = %+v, want explicit unlimited", effective.ChatRequestsPerDay)
	}

	// PATCH replaces the whole object: an omitted field returns to inheritance.
	if err := resolver.Save(context.Background(), "tenant-a", Overrides{}); err != nil {
		t.Fatalf("clear quota overrides: %v", err)
	}
	effective, err = resolver.Effective(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("effective quota after clear: %v", err)
	}
	if effective.MaxDocuments.Value != 10 || effective.MaxDocuments.Overridden {
		t.Fatalf("document limit after clear = %+v, want inherited default", effective.MaxDocuments)
	}
}

func TestResolverFailsForUnknownTenant(t *testing.T) {
	db := openQuotaTestDB(t)
	resolver := NewResolver(db, Limits{})
	if _, err := resolver.Effective(context.Background(), "missing"); err == nil {
		t.Fatal("unknown tenant must not silently resolve to deployment defaults")
	}
}

func openQuotaTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&models.Tenant{}); err != nil {
		t.Fatalf("migrate tenants: %v", err)
	}
	return db
}
