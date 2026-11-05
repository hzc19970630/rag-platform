package tenantdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type scopedRow struct {
	ID       string
	TenantID string
}

func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}
	return db
}

func TestScopedAddsTenantPredicate(t *testing.T) {
	db := dryRunDB(t)
	rows := []scopedRow{}
	query, err := Scoped(db, "tenant-a")
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	statement := query.Model(&scopedRow{}).Find(&rows).Statement
	if !strings.Contains(statement.SQL.String(), "tenant_id = ?") {
		t.Fatalf("expected tenant predicate, got %q", statement.SQL.String())
	}
	if len(statement.Vars) == 0 || statement.Vars[0] != "tenant-a" {
		t.Fatalf("expected tenant-a binding, got %#v", statement.Vars)
	}
}

func TestScopedRejectsEmptyTenant(t *testing.T) {
	db := dryRunDB(t)
	if _, err := Scoped(db, "  "); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("expected ErrTenantRequired, got %v", err)
	}
}

func TestMustScopedFailsClosed(t *testing.T) {
	db := dryRunDB(t)
	rows := []scopedRow{}
	statement := MustScoped(db, "").Model(&scopedRow{}).Find(&rows).Statement
	if !strings.Contains(statement.SQL.String(), "1 = 0") {
		t.Fatalf("expected fail-closed predicate, got %q", statement.SQL.String())
	}
}

func TestUnscopedKeepsQuery(t *testing.T) {
	db := dryRunDB(t)
	rows := []scopedRow{}
	statement := Unscoped(db, "maintenance sweep").Model(&scopedRow{}).Find(&rows).Statement
	if strings.Contains(statement.SQL.String(), "tenant_id = ?") {
		t.Fatalf("unscoped must not add a tenant predicate: %q", statement.SQL.String())
	}
}

func TestUnscopedRequiresReason(t *testing.T) {
	db := dryRunDB(t)
	rows := []scopedRow{}
	statement := Unscoped(db, "").Model(&scopedRow{}).Find(&rows).Statement
	if !strings.Contains(statement.SQL.String(), "1 = 0") {
		t.Fatalf("expected fail-closed predicate, got %q", statement.SQL.String())
	}
}

func TestTenantContext(t *testing.T) {
	ctx := WithTenant(context.Background(), "tenant-a")
	if tenantID, ok := TenantFrom(ctx); !ok || tenantID != "tenant-a" {
		t.Fatalf("expected tenant-a, got %q/%v", tenantID, ok)
	}
	if _, ok := TenantFrom(context.Background()); ok {
		t.Fatal("expected no tenant on an empty context")
	}
}
