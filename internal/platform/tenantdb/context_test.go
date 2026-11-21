package tenantdb

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSetContextValidatesTenantWithoutPostgres(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := SetContext(db, ""); err == nil {
		t.Fatalf("empty tenant must fail")
	}
	if err := SetContext(db, "tenant-a"); err != nil {
		t.Fatalf("sqlite set context = %v", err)
	}
}
