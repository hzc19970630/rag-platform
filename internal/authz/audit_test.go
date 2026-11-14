package authz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestAuditVisibilityFiltersAndPaging(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	service := &Service{auditStore: NewPostgresAuditStore(db)}
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []models.AuditLog{
		{TenantID: "tenant-a", ActorID: "viewer-a", Action: "read", ResourceType: "chunk", ResourceID: "v1", Allowed: false, Reason: "policy_denied", CreatedAt: now.Add(-4 * time.Minute)},
		{TenantID: "tenant-a", ActorID: "viewer-a", Action: "write", ResourceType: "knowledge_base", ResourceID: "v2", Allowed: true, Reason: "allowed", CreatedAt: now.Add(-3 * time.Minute)},
		{TenantID: "tenant-a", ActorID: "owner-a", Action: "write", ResourceType: "knowledge_base", ResourceID: "o1", Allowed: true, Reason: "allowed", CreatedAt: now.Add(-2 * time.Minute)},
		{TenantID: "tenant-b", ActorID: "owner-b", Action: "write", ResourceType: "knowledge_base", ResourceID: "b1", Allowed: true, Reason: "allowed", CreatedAt: now.Add(-time.Minute)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed audit rows: %v", err)
	}

	if _, err := service.QueryAudit(ctx, Principal{TenantID: "tenant-a", SubjectID: "ghost"}, AuditQuery{}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("non-member QueryAudit = %v, want forbidden", err)
	}

	viewerRows, err := service.QueryAudit(ctx, Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}, AuditQuery{})
	if err != nil {
		t.Fatalf("viewer QueryAudit: %v", err)
	}
	if len(viewerRows) != 2 {
		t.Fatalf("viewer rows = %d, want 2", len(viewerRows))
	}
	for _, row := range viewerRows {
		if row.ActorID != "viewer-a" {
			t.Fatalf("viewer saw another actor's row: %+v", row)
		}
	}

	// A viewer cannot widen the filter to another actor.
	widened, err := service.QueryAudit(ctx, Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}, AuditQuery{ActorID: "owner-a"})
	if err != nil {
		t.Fatalf("viewer filtered QueryAudit: %v", err)
	}
	if len(widened) != 2 {
		t.Fatalf("viewer actor filter returned %d rows, want own 2", len(widened))
	}

	owner := Principal{TenantID: "tenant-a", SubjectID: "owner-a"}
	ownerRows, err := service.QueryAudit(ctx, owner, AuditQuery{})
	if err != nil {
		t.Fatalf("owner QueryAudit: %v", err)
	}
	if len(ownerRows) != 3 {
		t.Fatalf("owner rows = %d, want 3", len(ownerRows))
	}

	filtered, err := service.QueryAudit(ctx, owner, AuditQuery{ActorID: "viewer-a"})
	if err != nil {
		t.Fatalf("owner filtered QueryAudit: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered rows = %d, want 2", len(filtered))
	}

	listed, err := service.ListAudit(ctx, owner, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("ListAudit rows = %d, want 3", len(listed))
	}

	page, err := service.QueryAuditPage(ctx, owner, AuditQuery{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("QueryAuditPage: %v", err)
	}
	if page.Total != 3 || page.Limit != 2 || page.Offset != 1 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
	if page.Items[0].ResourceID != "v2" || page.Items[1].ResourceID != "v1" {
		t.Fatalf("page rows = %+v", page.Items)
	}

	normalized, err := service.auditFilter(ctx, owner, AuditQuery{Limit: 0, Offset: -3})
	if err != nil {
		t.Fatalf("auditFilter: %v", err)
	}
	if normalized.Limit != 100 || normalized.Offset != 0 {
		t.Fatalf("normalized filter = %+v", normalized)
	}
	normalized, err = service.auditFilter(ctx, owner, AuditQuery{Limit: 501})
	if err != nil {
		t.Fatalf("auditFilter over limit: %v", err)
	}
	if normalized.Limit != 100 {
		t.Fatalf("over-limit filter = %+v", normalized)
	}
	normalized, err = service.auditFilter(ctx, owner, AuditQuery{Limit: 50, Offset: 2})
	if err != nil {
		t.Fatalf("auditFilter explicit: %v", err)
	}
	if normalized.Limit != 50 || normalized.Offset != 2 {
		t.Fatalf("explicit filter = %+v", normalized)
	}
}

func TestRecordAuditMarksApiKeyCredential(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	service := &Service{auditStore: NewPostgresAuditStore(db)}
	principal := Principal{
		TenantID:       "tenant-a",
		SubjectID:      "owner-a",
		CredentialType: "api_key",
		CredentialID:   "key-1",
	}
	if err := service.recordAudit(context.Background(), principal, "write", "/knowledge-bases/kb-a", false, "denied"); err != nil {
		t.Fatalf("recordAudit: %v", err)
	}
	var row models.AuditLog
	if err := db.Order("id DESC").First(&row).Error; err != nil {
		t.Fatalf("load audit row: %v", err)
	}
	if !strings.Contains(row.Reason, "denied;credential=api_key:key-1") {
		t.Fatalf("audit reason = %q", row.Reason)
	}
	if row.Allowed {
		t.Fatalf("audit row should be denied: %+v", row)
	}
}

func TestPurgeAuditLogsRejectsZeroCutoff(t *testing.T) {
	db := openAuthzDB(t)
	service := &Service{auditStore: NewPostgresAuditStore(db)}
	purged, err := service.PurgeAuditLogs(context.Background(), time.Time{}, 10)
	if err != nil {
		t.Fatalf("PurgeAuditLogs: %v", err)
	}
	if purged != 0 {
		t.Fatalf("purged = %d, want 0", purged)
	}
}
