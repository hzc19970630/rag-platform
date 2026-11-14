package tenantdb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRequestTransactionEnforcePaths(t *testing.T) {
	if err := ConfigureRLSMode(string(RLSModeEnforce)); err != nil {
		t.Fatalf("configure rls mode: %v", err)
	}
	defer func() { _ = ConfigureRLSMode(string(RLSModeShadow)) }()

	unreachable, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1"), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres dialector: %v", err)
	}
	if _, _, err := BeginRequestTx(context.Background(), unreachable, " "); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("empty tenant request tx = %v, want tenant required", err)
	}
	if _, finish, err := BeginRequestTx(context.Background(), unreachable, "tenant-1"); err == nil {
		finish(nil)
		t.Fatal("unreachable postgres must fail to begin the request transaction")
	}
	if err := InTx(context.Background(), unreachable, "tenant-1", func(*gorm.DB) error { return nil }); err == nil {
		t.Fatal("unreachable postgres must fail the service transaction")
	}

	fileDB, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "tx.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	tx := fileDB.Begin()
	if tx.Error != nil {
		t.Fatalf("begin sqlite: %v", tx.Error)
	}
	if err := setTenantContext(tx, "tenant-1"); err == nil {
		t.Fatal("set_config is PostgreSQL-only and must fail on SQLite")
	}
	_ = tx.Rollback().Error
}
