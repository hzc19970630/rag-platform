package inboxlease

import (
	"context"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newLeaseDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.TaskInbox{}); err != nil {
		t.Fatalf("migrate inbox: %v", err)
	}
	return db
}

func loadLeaseRow(t *testing.T, db *gorm.DB, key string) models.TaskInbox {
	t.Helper()
	var row models.TaskInbox
	if err := db.First(&row, "idempotency_key = ?", key).Error; err != nil {
		t.Fatalf("load inbox: %v", err)
	}
	return row
}

func TestRenewExtendsOnlyTheOwnerLease(t *testing.T) {
	db := newLeaseDB(t)
	ctx := context.Background()
	key := "run:parse"
	if claimed, err := Claim(ctx, db, key, "document.ingestion.v1", 0, New("owner-a", time.Second)); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	before := loadLeaseRow(t, db, key)
	renewed, err := Renew(ctx, db, key, New("owner-a", time.Minute))
	if err != nil || !renewed {
		t.Fatalf("owner renew = %v, %v", renewed, err)
	}
	after := loadLeaseRow(t, db, key)
	if after.LeaseUntil == nil || after.LeaseUntil.Before(*before.LeaseUntil) {
		t.Fatalf("renew did not extend the lease: %v -> %v", before.LeaseUntil, after.LeaseUntil)
	}

	renewed, err = Renew(ctx, db, key, New("owner-b", time.Minute))
	if err != nil {
		t.Fatalf("foreign renew: %v", err)
	}
	if renewed {
		t.Fatal("a worker that does not own the lease must not renew it")
	}
}

func TestReleaseNeverClearsAnotherWorkersLease(t *testing.T) {
	db := newLeaseDB(t)
	ctx := context.Background()
	key := "run:parse"
	if claimed, err := Claim(ctx, db, key, "document.ingestion.v1", 0, New("owner-a", time.Minute)); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := Release(ctx, db, key, "owner-b"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	row := loadLeaseRow(t, db, key)
	if row.LeaseOwner != "owner-a" || row.LeaseUntil == nil {
		t.Fatalf("foreign release cleared the lease: owner=%q until=%v", row.LeaseOwner, row.LeaseUntil)
	}
	if err := Release(ctx, db, key, "owner-a"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	row = loadLeaseRow(t, db, key)
	if row.LeaseOwner != "" || row.LeaseUntil != nil {
		t.Fatalf("owner release did not clear the lease: owner=%q until=%v", row.LeaseOwner, row.LeaseUntil)
	}
	if row.Status != StatusProcessing {
		t.Fatalf("release must not change the status, got %q", row.Status)
	}
}

func TestKeepRenewsUntilStopped(t *testing.T) {
	db := newLeaseDB(t)
	ctx := context.Background()
	key := "run:parse"
	lease := New("owner-a", 300*time.Millisecond)
	if claimed, err := Claim(ctx, db, key, "document.ingestion.v1", 0, lease); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	before := loadLeaseRow(t, db, key)
	stop := Keep(ctx, db, key, lease, func() { t.Error("lease unexpectedly lost") })
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		row := loadLeaseRow(t, db, key)
		if row.LeaseUntil != nil && row.LeaseUntil.After(before.LeaseUntil.Add(100*time.Millisecond)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat did not extend the lease: %v -> %v", before.LeaseUntil, row.LeaseUntil)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestKeepReportsLostLease(t *testing.T) {
	db := newLeaseDB(t)
	ctx := context.Background()
	key := "run:parse"
	lease := New("owner-a", 300*time.Millisecond)
	if claimed, err := Claim(ctx, db, key, "document.ingestion.v1", 0, lease); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	lost := make(chan struct{})
	stop := Keep(ctx, db, key, lease, func() { close(lost) })
	defer stop()
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", key).
		UpdateColumn("lease_owner", "owner-b").Error; err != nil {
		t.Fatalf("steal lease: %v", err)
	}
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not report the lost lease")
	}
}
