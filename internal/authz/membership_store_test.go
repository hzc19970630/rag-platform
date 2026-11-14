package authz

import (
	"context"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestPostgresMembershipStoreQueriesAndMutations(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	seedAuthzUsers(t, db)
	store := NewPostgresMembershipStore(db)
	ctx := context.Background()

	members, err := store.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("List tenant-a: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("tenant-a members = %+v, want 3", members)
	}
	byID := make(map[string]TenantMemberView, len(members))
	for _, member := range members {
		byID[member.UserID] = member
	}
	if byID["owner-a"].Email != "owner-a@example.test" || byID["admin-a"].Role != "admin" {
		t.Fatalf("joined member views = %+v", byID)
	}

	otherTenant, err := store.List(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("List tenant-b: %v", err)
	}
	if len(otherTenant) != 1 || otherTenant[0].UserID != "owner-b" {
		t.Fatalf("tenant-b members = %+v, want owner-b only", otherTenant)
	}

	user, ok, err := store.FindUser(ctx, "owner-a")
	if err != nil || !ok {
		t.Fatalf("FindUser(owner-a) = %v, %v", ok, err)
	}
	if user.Email != "owner-a@example.test" {
		t.Fatalf("FindUser email = %q", user.Email)
	}
	if _, ok, err := store.FindUser(ctx, "ghost"); err != nil || ok {
		t.Fatalf("FindUser(ghost) = %v, %v, want not found", ok, err)
	}

	membership, ok, err := store.Find(ctx, "tenant-a", "owner-a")
	if err != nil || !ok {
		t.Fatalf("Find(tenant-a, owner-a) = %v, %v", ok, err)
	}
	if membership.Role != "owner" {
		t.Fatalf("owner-a role = %q", membership.Role)
	}
	if _, ok, err := store.Find(ctx, "tenant-b", "owner-a"); err != nil || ok {
		t.Fatalf("cross-tenant Find = %v, %v, want not found", ok, err)
	}

	owners, err := store.CountOwners(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if owners != 1 {
		t.Fatalf("tenant-a owners = %d, want 1", owners)
	}

	if err := store.Save(ctx, models.Membership{TenantID: "tenant-a", UserID: "viewer-a", Role: "editor"}); err != nil {
		t.Fatalf("Save existing membership: %v", err)
	}
	updated, ok, err := store.Find(ctx, "tenant-a", "viewer-a")
	if err != nil || !ok {
		t.Fatalf("Find(viewer-a) = %v, %v", ok, err)
	}
	if updated.Role != "editor" {
		t.Fatalf("viewer-a role = %q, want editor", updated.Role)
	}

	if err := store.Save(ctx, models.Membership{TenantID: "tenant-a", UserID: "new-a", Role: "owner"}); err != nil {
		t.Fatalf("Save new membership: %v", err)
	}
	owners, err = store.CountOwners(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("CountOwners after insert: %v", err)
	}
	if owners != 2 {
		t.Fatalf("tenant-a owners = %d, want 2", owners)
	}
	otherOwners, err := store.CountOwners(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("CountOwners tenant-b: %v", err)
	}
	if otherOwners != 1 {
		t.Fatalf("tenant-b owners = %d, want 1", otherOwners)
	}
}
