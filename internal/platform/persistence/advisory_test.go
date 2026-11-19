package persistence

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLockResourceXactIsNoopOutsidePostgres(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return LockResourceXact(tx, "kb:tenant-a:kb-a", time.Second)
	}); err != nil {
		t.Fatalf("sqlite lock must be a no-op: %v", err)
	}
}
