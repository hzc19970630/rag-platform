package tenantdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func requestTxDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tx.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(2)
	if err := db.AutoMigrate(&scopedRow{}); err != nil {
		t.Fatalf("migrate rows: %v", err)
	}
	InstallRequestTxCallbacks(db)
	return db
}

func countRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&scopedRow{}).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestRequestTxRedirectsPooledWrites(t *testing.T) {
	db := requestTxDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	ctx := WithTx(context.Background(), tx)
	if err := db.WithContext(ctx).Create(&scopedRow{ID: "row-1", TenantID: "tenant-a"}).Error; err != nil {
		t.Fatalf("create through request tx: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if count := countRows(t, db); count != 0 {
		t.Fatalf("redirected write escaped the request transaction: %d rows", count)
	}
}

func TestRequestTxRedirectsScopedHandles(t *testing.T) {
	db := requestTxDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	ctx := WithTx(context.Background(), tx)
	scoped, err := Scoped(db.WithContext(ctx), "tenant-a")
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if err := scoped.Create(&scopedRow{ID: "row-2", TenantID: "tenant-a"}).Error; err != nil {
		t.Fatalf("create through scoped request tx: %v", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if count := countRows(t, db); count != 0 {
		t.Fatalf("scoped write escaped the request transaction: %d rows", count)
	}
}

func TestServiceTransactionIsNotRedirected(t *testing.T) {
	db := requestTxDB(t)
	requestTx := db.Begin()
	if requestTx.Error != nil {
		t.Fatalf("begin: %v", requestTx.Error)
	}
	defer func() { _ = requestTx.Rollback().Error }()
	ctx := WithTx(context.Background(), requestTx)
	err := db.WithContext(ctx).Transaction(func(serviceTx *gorm.DB) error {
		return serviceTx.Create(&scopedRow{ID: "row-3", TenantID: "tenant-a"}).Error
	})
	if err != nil {
		t.Fatalf("service transaction: %v", err)
	}
	if count := countRows(t, db); count != 1 {
		t.Fatalf("service transaction did not commit independently: %d rows", count)
	}
}

func TestInTxJoinsRequestTransaction(t *testing.T) {
	db := requestTxDB(t)
	requestTx := db.Begin()
	if requestTx.Error != nil {
		t.Fatalf("begin: %v", requestTx.Error)
	}
	ctx := WithTx(context.Background(), requestTx)
	if err := InTx(ctx, db, "tenant-a", func(tx *gorm.DB) error {
		return tx.Create(&scopedRow{ID: "row-3", TenantID: "tenant-a"}).Error
	}); err != nil {
		t.Fatalf("in tx: %v", err)
	}
	if err := requestTx.Rollback().Error; err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if count := countRows(t, db); count != 0 {
		t.Fatalf("InTx escaped the request transaction: %d rows", count)
	}
}

func TestInTxFailurePoisonsRequestTransaction(t *testing.T) {
	db := requestTxDB(t)
	requestTx := db.Begin()
	if requestTx.Error != nil {
		t.Fatalf("begin: %v", requestTx.Error)
	}
	defer func() { _ = requestTx.Rollback().Error }()
	ctx := WithTx(context.Background(), requestTx)
	errExpected := errors.New("boom")
	err := InTx(ctx, db, "tenant-a", func(*gorm.DB) error { return errExpected })
	if !errors.Is(err, errExpected) {
		t.Fatalf("expected boom, got %v", err)
	}
	state := txStateFrom(ctx)
	if state == nil || !state.rollback.Load() {
		t.Fatal("failed nested transaction must poison the request transaction")
	}
}

func TestInTxFallsBackOutsideEnforce(t *testing.T) {
	db := requestTxDB(t)
	called := false
	err := InTx(context.Background(), db, "tenant-a", func(tx *gorm.DB) error {
		called = true
		return tx.Create(&scopedRow{ID: "row-4", TenantID: "tenant-a"}).Error
	})
	if err != nil {
		t.Fatalf("in tx: %v", err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
	if count := countRows(t, db); count != 1 {
		t.Fatalf("shadow fallback should keep transaction semantics, got %d rows", count)
	}
}
