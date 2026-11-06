package authz

import (
	"context"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func TestTenantMemberChangesRevokeOnlyTargetSessions(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	now := time.Now().UTC()
	sessions := []models.AuthSession{
		{ID: "session-viewer-a", UserID: "viewer-a", TenantID: "tenant-a", TokenID: "token-viewer-a", ExpiresAt: now.Add(time.Hour), LastSeenAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "session-owner-a", UserID: "owner-a", TenantID: "tenant-a", TokenID: "token-owner-a", ExpiresAt: now.Add(time.Hour), LastSeenAt: now, CreatedAt: now, UpdatedAt: now},
		{ID: "session-viewer-b", UserID: "viewer-a", TenantID: "tenant-b", TokenID: "token-viewer-b", ExpiresAt: now.Add(time.Hour), LastSeenAt: now, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&sessions).Error; err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	service, err := NewService(NewPostgresStores(db))
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	ctx := context.Background()
	owner := Principal{SubjectID: "owner-a", TenantID: "tenant-a"}

	if err := service.UpdateTenantMember(ctx, owner, "viewer-a", "member"); err != nil {
		t.Fatalf("update tenant member: %v", err)
	}
	if revokedAt(t, db, "session-viewer-a") == nil {
		t.Fatal("role change must revoke the target member's sessions")
	}
	if revokedAt(t, db, "session-owner-a") != nil {
		t.Fatal("role change must not revoke another member's sessions")
	}
	if revokedAt(t, db, "session-viewer-b") != nil {
		t.Fatal("role change must not revoke the same user in another tenant")
	}

	// Re-saving the same role is not a role change and must keep the session.
	fresh := models.AuthSession{
		ID: "session-viewer-a-2", UserID: "viewer-a", TenantID: "tenant-a", TokenID: "token-viewer-a-2",
		ExpiresAt: now.Add(time.Hour), LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatalf("seed fresh session: %v", err)
	}
	if err := service.UpdateTenantMember(ctx, owner, "viewer-a", "member"); err != nil {
		t.Fatalf("re-save same role: %v", err)
	}
	if revokedAt(t, db, fresh.ID) != nil {
		t.Fatal("an unchanged role must not revoke sessions")
	}

	if err := service.RemoveTenantMember(ctx, owner, "viewer-a"); err != nil {
		t.Fatalf("remove tenant member: %v", err)
	}
	if revokedAt(t, db, fresh.ID) == nil {
		t.Fatal("member removal must revoke the member's sessions")
	}
}

func revokedAt(t *testing.T, db *gorm.DB, id string) *time.Time {
	t.Helper()
	var session models.AuthSession
	if err := db.First(&session, "id = ?", id).Error; err != nil {
		t.Fatalf("load session %s: %v", id, err)
	}
	return session.RevokedAt
}
