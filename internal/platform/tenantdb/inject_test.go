package tenantdb

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type injectTestRow struct {
	ID       string `gorm:"primaryKey;size:32"`
	TenantID string `gorm:"size:64;not null;index"`
	Name     string `gorm:"size:64"`
}

func (injectTestRow) TableName() string { return "chunks" }

func openInjectDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&injectTestRow{}); err != nil {
		t.Fatalf("migrate rows: %v", err)
	}
	if err := db.Create(&[]injectTestRow{
		{ID: "a-1", TenantID: "tenant-a", Name: "a"},
		{ID: "b-1", TenantID: "tenant-b", Name: "b"},
	}).Error; err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	InstallTenantScopeCallbacks(db)
	return db
}

func tenantContext(tenantID string) context.Context {
	return WithTenant(context.Background(), tenantID)
}

func TestTenantCallbackScopesQueries(t *testing.T) {
	db := openInjectDB(t)
	ResetScopeInjectionCount()

	var rows []injectTestRow
	if err := db.WithContext(tenantContext("tenant-a")).Find(&rows).Error; err != nil {
		t.Fatalf("scoped find: %v", err)
	}
	if len(rows) != 1 || rows[0].TenantID != "tenant-a" {
		t.Fatalf("callback must restrict reads to the context tenant: %+v", rows)
	}
	if ScopeInjectionCount() == 0 {
		t.Fatal("injection counter must record the scoped read")
	}

	rows = nil
	if err := db.WithContext(context.Background()).Find(&rows).Error; err != nil {
		t.Fatalf("unscoped context find: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("no tenant context must keep the query unchanged: %+v", rows)
	}
}

func TestTenantCallbackHonoursUnscopedBypass(t *testing.T) {
	db := openInjectDB(t)
	var rows []injectTestRow
	scoped := Unscoped(db.WithContext(tenantContext("tenant-a")), "test cross-tenant read")
	if err := scoped.Find(&rows).Error; err != nil {
		t.Fatalf("bypass find: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Unscoped must bypass tenant injection: %+v", rows)
	}
}

func TestTenantCallbackScopesUpdatesAndDeletes(t *testing.T) {
	db := openInjectDB(t)
	ctx := tenantContext("tenant-a")

	result := db.WithContext(ctx).Model(&injectTestRow{}).Where("id = ?", "b-1").Update("name", "changed")
	if result.Error != nil {
		t.Fatalf("cross-tenant update: %v", result.Error)
	}
	if result.RowsAffected != 0 {
		t.Fatalf("cross-tenant update affected %d rows", result.RowsAffected)
	}

	result = db.WithContext(ctx).Model(&injectTestRow{}).Where("id = ?", "a-1").Update("name", "changed")
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("tenant update = %d rows, err=%v", result.RowsAffected, result.Error)
	}

	result = db.WithContext(ctx).Where("id = ?", "b-1").Delete(&injectTestRow{})
	if result.Error != nil {
		t.Fatalf("cross-tenant delete: %v", result.Error)
	}
	if result.RowsAffected != 0 {
		t.Fatalf("cross-tenant delete affected %d rows", result.RowsAffected)
	}
}

func TestTenantCallbackFillsCreateAndRejectsMismatch(t *testing.T) {
	db := openInjectDB(t)
	ctx := tenantContext("tenant-a")

	row := injectTestRow{ID: "a-2", Name: "created"}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatalf("create with tenant context: %v", err)
	}
	if row.TenantID != "tenant-a" {
		t.Fatalf("create must fill TenantID, got %q", row.TenantID)
	}

	mismatch := injectTestRow{ID: "b-2", TenantID: "tenant-b", Name: "wrong"}
	err := db.WithContext(ctx).Create(&mismatch).Error
	if !errors.Is(err, ErrTenantScopeMismatch) {
		t.Fatalf("mismatched create error = %v, want ErrTenantScopeMismatch", err)
	}
}

func TestTenantCallbackKeepsExplicitPredicate(t *testing.T) {
	db := openInjectDB(t)
	var rows []injectTestRow
	err := db.WithContext(tenantContext("tenant-a")).
		Where("tenant_id = ?", "tenant-b").Find(&rows).Error
	if err != nil {
		t.Fatalf("explicit predicate find: %v", err)
	}
	if len(rows) != 1 || rows[0].TenantID != "tenant-b" {
		t.Fatalf("an explicit tenant predicate must not be overridden: %+v", rows)
	}
}

type anonymousRow struct {
	ID   string
	Name string
}

func TestTenantCallbackScopesTableOnlyQueries(t *testing.T) {
	db := openInjectDB(t)
	var rows []anonymousRow
	if err := db.WithContext(tenantContext("tenant-a")).Table("chunks").Find(&rows).Error; err != nil {
		t.Fatalf("table-only find: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "a-1" {
		t.Fatalf("table catalog must scope dest without TenantID: %+v", rows)
	}
}
