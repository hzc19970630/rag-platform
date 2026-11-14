package authz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func seedAuthzUsers(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	users := []models.User{
		{ID: "owner-a", Email: "owner-a@example.test", DisplayName: "Owner A"},
		{ID: "viewer-a", Email: "viewer-a@example.test", DisplayName: "Viewer A"},
		{ID: "admin-a", Email: "admin-a@example.test", DisplayName: "Admin A"},
		{ID: "owner-b", Email: "owner-b@example.test", DisplayName: "Owner B"},
		{ID: "new-a", Email: "new-a@example.test", DisplayName: "New A"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := db.Create(&models.Membership{TenantID: "tenant-a", UserID: "admin-a", Role: "admin"}).Error; err != nil {
		t.Fatalf("seed admin membership: %v", err)
	}
}

func newAuthzFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db := openAuthzDB(t)
	seedAuthz(t, db)
	seedAuthzUsers(t, db)
	service, err := NewService(NewPostgresStores(db))
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	return service, db
}

func TestRequireTenantOperatorRejectsApiKeysAndNonAdmins(t *testing.T) {
	service, _ := newAuthzFixture(t)
	ctx := context.Background()
	tests := []struct {
		name      string
		principal Principal
		want      error
	}{
		{
			name:      "missing tenant",
			principal: Principal{SubjectID: "owner-a"},
			want:      apperror.ErrUnauthenticated,
		},
		{
			name:      "missing subject",
			principal: Principal{TenantID: "tenant-a"},
			want:      apperror.ErrUnauthenticated,
		},
		{
			name: "api key is forbidden even with management capability",
			principal: Principal{
				TenantID:       "tenant-a",
				SubjectID:      "owner-a",
				CredentialType: "api_key",
				Capabilities:   []string{"knowledge_base:manage_members"},
			},
			want: apperror.ErrForbidden,
		},
		{
			name:      "viewer is forbidden",
			principal: Principal{TenantID: "tenant-a", SubjectID: "viewer-a"},
			want:      apperror.ErrForbidden,
		},
		{
			name:      "admin is allowed",
			principal: Principal{TenantID: "tenant-a", SubjectID: "admin-a"},
		},
		{
			name:      "owner is allowed",
			principal: Principal{TenantID: "tenant-a", SubjectID: "owner-a"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := service.RequireTenantOperator(ctx, test.principal)
			if test.want == nil {
				if err != nil {
					t.Fatalf("RequireTenantOperator returned %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("RequireTenantOperator = %v, want %v", err, test.want)
			}
		})
	}
}

func TestListTenantMembersIsTenantScoped(t *testing.T) {
	service, _ := newAuthzFixture(t)
	ctx := context.Background()

	if _, err := service.ListTenantMembers(ctx, Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer ListTenantMembers = %v, want forbidden", err)
	}

	members, err := service.ListTenantMembers(ctx, Principal{TenantID: "tenant-a", SubjectID: "owner-a"})
	if err != nil {
		t.Fatalf("owner ListTenantMembers: %v", err)
	}
	byID := make(map[string]TenantMemberView, len(members))
	for _, member := range members {
		byID[member.UserID] = member
	}
	if len(members) != 3 {
		t.Fatalf("tenant-a members = %+v, want 3", members)
	}
	if byID["viewer-a"].Email != "viewer-a@example.test" || byID["viewer-a"].Role != "viewer" {
		t.Fatalf("viewer-a view = %+v", byID["viewer-a"])
	}

	other, err := service.ListTenantMembers(ctx, Principal{TenantID: "tenant-b", SubjectID: "owner-b"})
	if err != nil {
		t.Fatalf("owner-b ListTenantMembers: %v", err)
	}
	if len(other) != 1 || other[0].UserID != "owner-b" {
		t.Fatalf("tenant-b members = %+v, want owner-b only", other)
	}
}

func TestAddTenantMemberAuthorizationAndValidation(t *testing.T) {
	t.Run("viewer cannot add members", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		_, err := service.AddTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}, "new-a", "member")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("AddTenantMember = %v, want forbidden", err)
		}
	})

	t.Run("unknown role is a validation error", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		_, err := service.AddTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "new-a", "root")
		if _, ok := apperror.AsValidation(err); !ok {
			t.Fatalf("AddTenantMember = %v, want validation error", err)
		}
	})

	t.Run("admin cannot grant owner", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		_, err := service.AddTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "admin-a"}, "new-a", "owner")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("AddTenantMember = %v, want forbidden", err)
		}
	})

	t.Run("unknown user is not found", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		_, err := service.AddTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "ghost", "member")
		if !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("AddTenantMember = %v, want not found", err)
		}
	})

	t.Run("owner adds a member and the revision advances", func(t *testing.T) {
		service, db := newAuthzFixture(t)
		ctx := context.Background()
		before, err := service.policyStore.CurrentRevision(ctx, "tenant-a")
		if err != nil {
			t.Fatalf("current revision: %v", err)
		}
		member, err := service.AddTenantMember(ctx, Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "new-a", "member")
		if err != nil {
			t.Fatalf("AddTenantMember: %v", err)
		}
		if member.UserID != "new-a" || member.Email != "new-a@example.test" || member.Role != "member" {
			t.Fatalf("member view = %+v", member)
		}
		after, err := service.policyStore.CurrentRevision(ctx, "tenant-a")
		if err != nil {
			t.Fatalf("current revision after add: %v", err)
		}
		if after <= before {
			t.Fatalf("revision = %d after add, want > %d", after, before)
		}
		var stored models.Membership
		if err := db.First(&stored, "tenant_id = ? AND user_id = ?", "tenant-a", "new-a").Error; err != nil {
			t.Fatalf("load membership: %v", err)
		}
		if stored.Role != "member" {
			t.Fatalf("stored role = %q, want member", stored.Role)
		}
		var audit models.AuditLog
		if err := db.Where("action = ? AND resource_id = ?", "add_member", "new-a").First(&audit).Error; err != nil {
			t.Fatalf("load add_member audit: %v", err)
		}
		if audit.Allowed != true || audit.Reason != "role=member" {
			t.Fatalf("audit row = %+v", audit)
		}
	})
}

func TestUpdateTenantMemberGuardsLastOwner(t *testing.T) {
	t.Run("viewer cannot update", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.UpdateTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}, "viewer-a", "member")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("UpdateTenantMember = %v, want forbidden", err)
		}
	})

	t.Run("invalid role", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.UpdateTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "viewer-a", "root")
		if _, ok := apperror.AsValidation(err); !ok {
			t.Fatalf("UpdateTenantMember = %v, want validation error", err)
		}
	})

	t.Run("admin cannot promote to owner", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.UpdateTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "admin-a"}, "viewer-a", "owner")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("UpdateTenantMember = %v, want forbidden", err)
		}
	})

	t.Run("unknown member", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.UpdateTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "ghost", "member")
		if !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("UpdateTenantMember = %v, want not found", err)
		}
	})

	t.Run("last owner cannot be demoted", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.UpdateTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "owner-a", "admin")
		if err == nil || !strings.Contains(err.Error(), "last tenant owner") {
			t.Fatalf("UpdateTenantMember = %v, want last-owner guard", err)
		}
	})

	t.Run("owner transfer allows demoting the original owner", func(t *testing.T) {
		service, db := newAuthzFixture(t)
		ctx := context.Background()
		if err := service.UpdateTenantMember(ctx, Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "admin-a", "owner"); err != nil {
			t.Fatalf("promote admin to owner: %v", err)
		}
		if err := service.UpdateTenantMember(ctx, Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "owner-a", "admin"); err != nil {
			t.Fatalf("demote original owner: %v", err)
		}
		var updated models.Membership
		if err := db.First(&updated, "tenant_id = ? AND user_id = ?", "tenant-a", "owner-a").Error; err != nil {
			t.Fatalf("load demoted membership: %v", err)
		}
		if updated.Role != "admin" {
			t.Fatalf("role = %q, want admin", updated.Role)
		}
		var audit models.AuditLog
		if err := db.Where("action = ? AND resource_id = ?", "update_member", "owner-a").First(&audit).Error; err != nil {
			t.Fatalf("load update_member audit: %v", err)
		}
	})
}

func TestRemoveTenantMemberGuardsLastOwner(t *testing.T) {
	t.Run("viewer cannot remove", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.RemoveTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}, "viewer-a")
		if !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("RemoveTenantMember = %v, want forbidden", err)
		}
	})

	t.Run("unknown member", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.RemoveTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "ghost")
		if !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("RemoveTenantMember = %v, want not found", err)
		}
	})

	t.Run("last owner cannot be removed", func(t *testing.T) {
		service, _ := newAuthzFixture(t)
		err := service.RemoveTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "owner-a")
		if err == nil || !strings.Contains(err.Error(), "last tenant owner") {
			t.Fatalf("RemoveTenantMember = %v, want last-owner guard", err)
		}
	})

	t.Run("owner removes a member", func(t *testing.T) {
		service, db := newAuthzFixture(t)
		if err := service.RemoveTenantMember(context.Background(), Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, "viewer-a"); err != nil {
			t.Fatalf("RemoveTenantMember: %v", err)
		}
		var count int64
		if err := db.Model(&models.Membership{}).Where("tenant_id = ? AND user_id = ?", "tenant-a", "viewer-a").Count(&count).Error; err != nil {
			t.Fatalf("count removed membership: %v", err)
		}
		if count != 0 {
			t.Fatalf("membership rows = %d, want 0", count)
		}
		var audit models.AuditLog
		if err := db.Where("action = ? AND resource_id = ?", "remove_member", "viewer-a").First(&audit).Error; err != nil {
			t.Fatalf("load remove_member audit: %v", err)
		}
	})
}

func TestIsOwner(t *testing.T) {
	service, _ := newAuthzFixture(t)
	ctx := context.Background()
	if !service.isOwner(ctx, Principal{TenantID: "tenant-a", SubjectID: "owner-a"}) {
		t.Fatal("owner-a must be an owner")
	}
	if service.isOwner(ctx, Principal{TenantID: "tenant-a", SubjectID: "viewer-a"}) {
		t.Fatal("viewer-a must not be an owner")
	}
	if service.isOwner(ctx, Principal{TenantID: "tenant-a", SubjectID: "ghost"}) {
		t.Fatal("unknown member must not be an owner")
	}
}
