package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeLDAP struct {
	users       []LDAPIdentity
	roles       map[string]string
	defaultRole string
}

func (f *fakeLDAP) Authenticate(_ context.Context, username, password string) (LDAPIdentity, error) {
	if password == "" {
		return LDAPIdentity{}, apperror.ErrUnauthenticated
	}
	for _, user := range f.users {
		if user.Username == username {
			return user, nil
		}
	}
	return LDAPIdentity{}, apperror.ErrUnauthenticated
}

func (f *fakeLDAP) ListUsers(context.Context) ([]LDAPIdentity, error) {
	return append([]LDAPIdentity(nil), f.users...), nil
}

func (f *fakeLDAP) RoleForGroups(groups []string) (string, bool) {
	order := map[string]int{"owner": 4, "admin": 3, "member": 2, "viewer": 1}
	best, rank := "", 0
	for _, group := range groups {
		if role, ok := f.roles[group]; ok && order[role] > rank {
			best, rank = role, order[role]
		}
	}
	if best != "" {
		return best, true
	}
	if f.defaultRole != "" {
		return f.defaultRole, true
	}
	return "", false
}

func newLDAPTestService(t *testing.T, directory *fakeLDAP) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.User{}, &models.Tenant{}, &models.Membership{}, &models.AuthSession{}, &models.AuditLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now()
	if err := db.Create(&models.Tenant{ID: "tenant-a", Name: "Tenant A", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	service, err := NewService(db, AuthOptions{
		SigningKeys:       []string{"test:0123456789abcdef"},
		ActiveKeyID:       "test",
		SessionTTLSeconds: 3600,
		JWTIssuer:         "rag-platform",
		JWTAudience:       "rag-platform-api",
		LDAP:              LDAPOptions{Enabled: true, TenantID: "tenant-a", AutoProvision: true},
	})
	if err != nil {
		t.Fatalf("new identity service: %v", err)
	}
	service.ldap = directory
	return service
}

func TestLoginLDAPMapsGroupsToRole(t *testing.T) {
	directory := &fakeLDAP{
		users: []LDAPIdentity{{
			DN: "cn=alice,ou=people,dc=example,dc=com", Username: "alice", Email: "alice@example.com",
			Name: "Alice", Groups: []string{"cn=rag-admins,ou=groups,dc=example,dc=com"}, TenantID: "tenant-a",
		}},
		roles: map[string]string{"cn=rag-admins,ou=groups,dc=example,dc=com": "admin"},
	}
	service := newLDAPTestService(t, directory)
	result, err := service.LoginLDAP(context.Background(), "alice", "secret", "test", "127.0.0.1")
	if err != nil {
		t.Fatalf("ldap login: %v", err)
	}
	if result.Token == "" || result.Principal.SubjectID == "" {
		t.Fatalf("session result = %+v", result)
	}
	var membership models.Membership
	if err := service.db.First(&membership, "tenant_id = ? AND user_id = ?", "tenant-a", result.Principal.SubjectID).Error; err != nil {
		t.Fatalf("load membership: %v", err)
	}
	if membership.Role != "admin" {
		t.Fatalf("membership role = %q, want admin", membership.Role)
	}
	var user models.User
	if err := service.db.First(&user, "id = ?", result.Principal.SubjectID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.LDAPDN == "" {
		t.Fatal("ldap_dn was not persisted")
	}
}

func TestLoginLDAPRejectsUnmappedUser(t *testing.T) {
	directory := &fakeLDAP{
		users: []LDAPIdentity{{
			DN: "cn=bob,ou=people,dc=example,dc=com", Username: "bob", Email: "bob@example.com",
			Groups: []string{"cn=other,ou=groups,dc=example,dc=com"}, TenantID: "tenant-a",
		}},
		roles: map[string]string{"cn=rag-admins,ou=groups,dc=example,dc=com": "admin"},
	}
	service := newLDAPTestService(t, directory)
	if _, err := service.LoginLDAP(context.Background(), "bob", "secret", "test", "127.0.0.1"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("unmapped login err = %v, want unauthenticated", err)
	}
}

func TestSyncLDAPUpdatesRolesAndDeprovisions(t *testing.T) {
	directory := &fakeLDAP{
		users: []LDAPIdentity{
			{DN: "cn=alice,ou=people,dc=example,dc=com", Username: "alice", Email: "alice@example.com", Groups: []string{"admins"}, TenantID: "tenant-a"},
			{DN: "cn=bob,ou=people,dc=example,dc=com", Username: "bob", Email: "bob@example.com", Groups: []string{"viewers"}, TenantID: "tenant-a"},
		},
		roles: map[string]string{"admins": "admin", "viewers": "viewer"},
	}
	service := newLDAPTestService(t, directory)
	if _, err := service.SyncLDAP(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	var bob models.User
	if err := service.db.First(&bob, "email = ?", "bob@example.com").Error; err != nil {
		t.Fatalf("load bob: %v", err)
	}
	if _, err := service.LoginLDAP(context.Background(), "bob", "secret", "test", "127.0.0.1"); err != nil {
		t.Fatalf("bob login: %v", err)
	}

	directory.users = directory.users[:1]
	result, err := service.SyncLDAP(context.Background())
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if result.Removed != 1 {
		t.Fatalf("removed = %d, want 1", result.Removed)
	}
	var memberships int64
	service.db.Model(&models.Membership{}).Where("user_id = ?", bob.ID).Count(&memberships)
	if memberships != 0 {
		t.Fatalf("bob membership still present")
	}
	var active int64
	service.db.Model(&models.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", bob.ID).Count(&active)
	if active != 0 {
		t.Fatalf("bob still has %d active sessions", active)
	}
}

func TestLDAPGroupRoleMapping(t *testing.T) {
	client := NewLDAPClient(LDAPOptions{
		Enabled:      true,
		GroupRoleMap: "cn=rag-admins,ou=groups,dc=example,dc=com=admin;rag-viewers=viewer",
	})
	if role, ok := client.RoleForGroups([]string{"CN=RAG-Admins,OU=Groups,DC=example,DC=com"}); !ok || role != "admin" {
		t.Fatalf("admin mapping = %q/%v", role, ok)
	}
	if role, ok := client.RoleForGroups([]string{"cn=rag-viewers,ou=groups,dc=example,dc=com"}); !ok || role != "viewer" {
		t.Fatalf("viewer mapping = %q/%v", role, ok)
	}
	if _, ok := client.RoleForGroups([]string{"cn=unknown,ou=groups"}); ok {
		t.Fatal("unmapped group must be rejected without a default role")
	}
}
