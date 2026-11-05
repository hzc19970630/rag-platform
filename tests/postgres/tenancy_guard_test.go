// Package postgres_test contains tests that need a real PostgreSQL instance.
// Run them with TEST_POSTGRES_URL set; they are skipped otherwise so the
// default SQLite test suite stays self-contained.
package postgres_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type guardRow struct {
	ID       string `gorm:"column:id;primaryKey"`
	TenantID string `gorm:"column:tenant_id"`
}

func openGuardDB(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap postgres connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func guardTable(t *testing.T, db *gorm.DB) string {
	t.Helper()
	table := fmt.Sprintf("tenancy_guard_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE TABLE " + table + " (id VARCHAR(64) PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL)").Error; err != nil {
		t.Fatalf("create guard table: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS " + table).Error })
	if err := db.Table(table).Create(&[]guardRow{
		{ID: "a-1", TenantID: "tenant-a"},
		{ID: "b-1", TenantID: "tenant-b"},
	}).Error; err != nil {
		t.Fatalf("seed guard table: %v", err)
	}
	return table
}

func TestScopedQueryCannotSeeAnotherTenant(t *testing.T) {
	db := openGuardDB(t)
	table := guardTable(t, db)

	scoped, err := tenantdb.Scoped(db.Table(table), "tenant-a")
	if err != nil {
		t.Fatalf("scope query: %v", err)
	}
	var rows []guardRow
	if err := scoped.Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("query scoped rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "a-1" {
		t.Fatalf("expected only tenant-a rows, got %#v", rows)
	}
}

func TestScopedRejectsEmptyTenant(t *testing.T) {
	db := openGuardDB(t)
	table := guardTable(t, db)

	if _, err := tenantdb.Scoped(db.Table(table), "  "); !errors.Is(err, tenantdb.ErrTenantRequired) {
		t.Fatalf("expected ErrTenantRequired, got %v", err)
	}
	var rows []guardRow
	if err := tenantdb.MustScoped(db.Table(table), "").Find(&rows).Error; err != nil {
		t.Fatalf("fail-closed query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("fail-closed query returned rows: %#v", rows)
	}
}

func TestUnscopedQueryCrossesTenants(t *testing.T) {
	db := openGuardDB(t)
	table := guardTable(t, db)

	var rows []guardRow
	if err := tenantdb.Unscoped(db.Table(table), "test: verify the explicit bypass").Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("query unscoped rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both tenants' rows, got %#v", rows)
	}
	var empty []guardRow
	if err := tenantdb.Unscoped(db.Table(table), "").Find(&empty).Error; err != nil {
		t.Fatalf("reasonless bypass must not error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("reasonless bypass returned rows: %#v", empty)
	}
}
