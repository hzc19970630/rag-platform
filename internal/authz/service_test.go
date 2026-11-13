package authz

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCapabilitiesForRole(t *testing.T) {
	tests := []struct {
		role string
		want []string
	}{
		{role: "owner", want: []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members", "tenant:admin"}},
		{role: "admin", want: []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members", "tenant:admin"}},
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

	service := &Service{auditStore: NewPostgresAuditStore(db)}
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

func TestQueryAuditPageReturnsTotalAndOffset(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:audit-page?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.AuditLog{}, &models.Membership{}); err != nil {
		t.Fatalf("migrate audit tables: %v", err)
	}
	if err := db.Create(&models.Membership{TenantID: "tenant-1", UserID: "user-1", Role: "owner"}).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	now := time.Now().UTC()
	seed := []models.AuditLog{
		auditRow("a", now.Add(-4*time.Minute)),
		auditRow("b", now.Add(-3*time.Minute)),
		auditRow("c", now.Add(-2*time.Minute)),
		auditRow("d", now.Add(-time.Minute)),
		auditRow("e", now),
	}
	for index := range seed {
		seed[index].Action = "write"
	}
	seed[0].Action = "read"
	if err := db.Create(&seed).Error; err != nil {
		t.Fatalf("seed audit rows: %v", err)
	}

	service := &Service{auditStore: NewPostgresAuditStore(db)}
	principal := Principal{TenantID: "tenant-1", SubjectID: "user-1"}
	page, err := service.QueryAuditPage(context.Background(), principal, AuditQuery{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("query audit page: %v", err)
	}
	if page.Total != 5 || page.Limit != 2 || page.Offset != 1 {
		t.Fatalf("page metadata = %+v", page)
	}
	if len(page.Items) != 2 || page.Items[0].ResourceID != "d" || page.Items[1].ResourceID != "c" {
		t.Fatalf("page rows = %+v", page.Items)
	}

	filtered, err := service.QueryAuditPage(context.Background(), principal, AuditQuery{Action: "read", Limit: 20})
	if err != nil {
		t.Fatalf("query filtered audit page: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].ResourceID != "a" {
		t.Fatalf("filtered page = %+v", filtered)
	}
}
