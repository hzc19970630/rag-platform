package authz

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCapabilitiesForRole(t *testing.T) {
	tests := []struct {
		role string
		want []string
	}{
		{role: "owner", want: []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members"}},
		{role: "admin", want: []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members"}},
		{role: "editor", want: []string{"knowledge_base:read", "knowledge_base:write"}},
		{role: "viewer", want: []string{"knowledge_base:read"}},
	}
	for _, test := range tests {
		if got := capabilitiesForRole(test.role); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("role %q capabilities = %#v, want %#v", test.role, got, test.want)
		}
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"kb-a", "kb-b"}, "kb-b") {
		t.Fatal("expected value to be found")
	}
	if contains([]string{"kb-a"}, "kb-b") {
		t.Fatal("did not expect value to be found")
	}
}

func TestAuditRequired(t *testing.T) {
	cases := []struct {
		action  string
		allowed bool
		want    bool
	}{
		{action: "read", allowed: true, want: false},
		{action: "READ", allowed: true, want: false},
		{action: "read", allowed: false, want: true},
		{action: "write", allowed: true, want: true},
		{action: "manage_members", allowed: true, want: true},
		{action: "delete", allowed: false, want: true},
	}
	for _, test := range cases {
		if got := auditRequired(test.action, test.allowed); got != test.want {
			t.Fatalf("auditRequired(%q, %v) = %v, want %v", test.action, test.allowed, got, test.want)
		}
	}
}

func auditRow(resourceID string, createdAt time.Time) models.AuditLog {
	return models.AuditLog{
		TenantID: "tenant-1", ActorID: "user-1", Action: "read", ResourceType: "chunk",
		ResourceID: resourceID, Allowed: false, Reason: "policy_denied", CreatedAt: createdAt,
	}
}

func TestPurgeAuditLogsDeletesOnlyExpiredRowsInBatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.AuditLog{}); err != nil {
		t.Fatalf("migrate audit logs: %v", err)
	}
	now := time.Now().UTC()
	rows := []models.AuditLog{
		auditRow("old-1", now.Add(-72*time.Hour)),
		auditRow("old-2", now.Add(-48*time.Hour)),
		auditRow("old-3", now.Add(-25*time.Hour)),
		auditRow("new", now.Add(-time.Hour)),
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed audit rows: %v", err)
	}

	service := &Service{db: db}
	ctx := context.Background()
	purged, err := service.PurgeAuditLogs(ctx, now.Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatalf("purge audit logs: %v", err)
	}
	if purged != 2 {
		t.Fatalf("expected one batch of 2 rows, got %d", purged)
	}
	purged, err = service.PurgeAuditLogs(ctx, now.Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatalf("purge remaining audit logs: %v", err)
	}
	if purged != 1 {
		t.Fatalf("expected the last expired row, got %d", purged)
	}
	var remaining []models.AuditLog
	if err := db.Order("id").Find(&remaining).Error; err != nil {
		t.Fatalf("load remaining audit rows: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ResourceID != "new" {
		t.Fatalf("only the non-expired row must remain, got %+v", remaining)
	}
}
