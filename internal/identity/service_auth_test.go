package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIdentityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open identity db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("identity db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.User{}, &models.Tenant{}, &models.Membership{},
		&models.AuthSession{}, &models.AuditLog{}, &models.APIKey{}, &models.KnowledgeBase{},
	); err != nil {
		t.Fatalf("migrate identity tables: %v", err)
	}
	return db
}

func seedIdentityDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Create(&[]models.Tenant{
		{ID: "tenant-a", Name: "Tenant A"},
		{ID: "tenant-42", Name: "OIDC Tenant"},
	}).Error; err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	if err := db.Create(&[]models.User{
		{ID: "owner-a", Email: "owner-a@example.test", DisplayName: "Owner A"},
		{ID: "viewer-a", Email: "viewer-a@example.test", DisplayName: "Viewer A"},
	}).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner"},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer"},
	}).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := db.Create(&[]models.KnowledgeBase{
		{ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB A"},
		{ID: "kb-b", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB B"},
	}).Error; err != nil {
		t.Fatalf("seed knowledge bases: %v", err)
	}
}

func newIdentityAuthService(t *testing.T, db *gorm.DB, mutate func(*AuthOptions)) *Service {
	t.Helper()
	options := AuthOptions{
		SigningKeys:       []string{"test:secret"},
		ActiveKeyID:       "test",
		SessionTTLSeconds: 3600,
		APIKeyPepper:      "test-pepper",
		DevAuthEnabled:    true,
	}
	if mutate != nil {
		mutate(&options)
	}
	service, err := NewService(db, options)
	if err != nil {
		t.Fatalf("new identity service: %v", err)
	}
	return service
}

func authzPrincipal(subjectID, tenantID string) authz.Principal {
	return authz.Principal{SubjectID: subjectID, TenantID: tenantID}
}

func timePointer(value time.Time) *time.Time { return &value }

func TestLoginDevelopmentSessionLifecycle(t *testing.T) {
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	service := newIdentityAuthService(t, db, nil)
	ctx := context.Background()

	result, err := service.LoginDevelopment(ctx, "owner-a", "tenant-a", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("LoginDevelopment: %v", err)
	}
	if result.Token == "" || result.Principal.SubjectID != "owner-a" || result.Principal.TenantID != "tenant-a" {
		t.Fatalf("session result = %+v", result)
	}
	principal, err := service.Authenticate(ctx, result.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.SubjectID != "owner-a" || principal.CredentialType != "session" {
		t.Fatalf("principal = %+v", principal)
	}

	refreshed, expiresAt, err := service.Refresh(ctx, result.Token)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed == "" || !expiresAt.After(time.Now()) {
		t.Fatalf("refreshed token = %q expiresAt = %v", refreshed, expiresAt)
	}
	if _, err := service.Authenticate(ctx, refreshed); err != nil {
		t.Fatalf("Authenticate refreshed token: %v", err)
	}

	if err := service.RevokeSession(ctx, refreshed); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := service.Authenticate(ctx, refreshed); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Authenticate after revoke = %v, want unauthenticated", err)
	}
	if _, _, err := service.Refresh(ctx, refreshed); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Refresh after revoke = %v, want unauthenticated", err)
	}
	if err := service.RevokeSession(ctx, refreshed); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("RevokeSession twice = %v, want unauthenticated", err)
	}

	if _, err := service.LoginDevelopment(ctx, "ghost", "tenant-a", "", ""); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("LoginDevelopment unknown user = %v, want forbidden", err)
	}
	if err := db.Create(&models.Membership{TenantID: "tenant-a", UserID: "missing-user", Role: "member"}).Error; err != nil {
		t.Fatalf("seed orphan membership: %v", err)
	}
	if _, err := service.LoginDevelopment(ctx, "missing-user", "tenant-a", "", ""); err == nil || !strings.Contains(err.Error(), "load development user") {
		t.Fatalf("LoginDevelopment orphan membership = %v, want user load error", err)
	}
	disabled := newIdentityAuthService(t, db, func(options *AuthOptions) { options.DevAuthEnabled = false })
	if _, err := disabled.LoginDevelopment(ctx, "owner-a", "tenant-a", "", ""); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("LoginDevelopment disabled = %v, want unauthenticated", err)
	}

	var audits int64
	if err := db.Model(&models.AuditLog{}).Where("action = ? AND actor_id = ?", "login", "owner-a").Count(&audits).Error; err != nil {
		t.Fatalf("count login audits: %v", err)
	}
	if audits == 0 {
		t.Fatal("expected a login audit row")
	}

	longAgent := strings.Repeat("a", 600)
	truncated, err := service.LoginDevelopment(ctx, "owner-a", "tenant-a", longAgent, "127.0.0.1")
	if err != nil {
		t.Fatalf("LoginDevelopment long user agent: %v", err)
	}
	var session models.AuthSession
	if err := db.First(&session, "id = ?", truncated.Principal.CredentialID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(session.UserAgent) != 512 || truncated.Token == "" {
		t.Fatalf("truncated user agent length = %d", len(session.UserAgent))
	}
}

func TestAuthenticateRejectsExpiredRevokedAndForgedTokens(t *testing.T) {
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	service := newIdentityAuthService(t, db, nil)
	ctx := context.Background()

	expired, err := service.LoginDevelopment(ctx, "viewer-a", "tenant-a", "", "")
	if err != nil {
		t.Fatalf("LoginDevelopment: %v", err)
	}
	if err := db.Model(&models.AuthSession{}).Where("user_id = ?", "viewer-a").
		Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire session: %v", err)
	}
	if _, err := service.Authenticate(ctx, expired.Token); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Authenticate expired = %v, want unauthenticated", err)
	}

	revoked, err := service.LoginDevelopment(ctx, "owner-a", "tenant-a", "", "")
	if err != nil {
		t.Fatalf("LoginDevelopment owner: %v", err)
	}
	if err := db.Model(&models.AuthSession{}).Where("user_id = ?", "owner-a").
		Update("revoked_at", time.Now()).Error; err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := service.Authenticate(ctx, revoked.Token); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Authenticate revoked = %v, want unauthenticated", err)
	}

	if _, err := service.Authenticate(ctx, "not-a-token"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Authenticate forged = %v, want unauthenticated", err)
	}
	if _, _, err := service.Refresh(ctx, "not-a-token"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Refresh forged = %v, want unauthenticated", err)
	}
	if err := service.RevokeSession(ctx, "not-a-token"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("RevokeSession forged = %v, want unauthenticated", err)
	}

	subjectMismatch, err := service.LoginDevelopment(ctx, "owner-a", "tenant-a", "", "")
	if err != nil {
		t.Fatalf("LoginDevelopment subject mismatch: %v", err)
	}
	if err := db.Model(&models.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", "owner-a").
		Update("user_id", "viewer-a").Error; err != nil {
		t.Fatalf("mismatch session subject: %v", err)
	}
	if _, err := service.Authenticate(ctx, subjectMismatch.Token); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Authenticate subject mismatch = %v, want unauthenticated", err)
	}

	tenantMismatch, err := service.LoginDevelopment(ctx, "viewer-a", "tenant-a", "", "")
	if err != nil {
		t.Fatalf("LoginDevelopment tenant mismatch: %v", err)
	}
	if err := db.Model(&models.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", "viewer-a").
		Update("tenant_id", "tenant-42").Error; err != nil {
		t.Fatalf("mismatch session tenant: %v", err)
	}
	if _, err := service.Authenticate(ctx, tenantMismatch.Token); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Authenticate tenant mismatch = %v, want unauthenticated", err)
	}

	refreshExpired, err := service.LoginDevelopment(ctx, "owner-a", "tenant-a", "", "")
	if err != nil {
		t.Fatalf("LoginDevelopment refresh expired: %v", err)
	}
	if err := db.Model(&models.AuthSession{}).Where("user_id = ? AND revoked_at IS NULL", "owner-a").
		Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire refresh session: %v", err)
	}
	if _, _, err := service.Refresh(ctx, refreshExpired.Token); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("Refresh expired session = %v, want unauthenticated", err)
	}
}

func TestAPIKeyLifecycleAndValidation(t *testing.T) {
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	service := newIdentityAuthService(t, db, nil)
	ctx := context.Background()
	owner := authzPrincipal("owner-a", "tenant-a")
	viewer := authzPrincipal("viewer-a", "tenant-a")

	if _, _, err := service.CreateAPIKey(ctx, viewer, CreateAPIKeyInput{Name: "viewer-key"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer CreateAPIKey = %v, want forbidden", err)
	}
	if _, _, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{}); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if _, _, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{Name: "bad", Capabilities: []string{"tenant:admin"}}); err == nil {
		t.Fatal("unknown capability must be rejected")
	}
	past := time.Now().Add(-time.Hour)
	if _, _, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{Name: "past", ExpiresAt: &past}); err == nil {
		t.Fatal("past expiry must be rejected")
	}
	if _, _, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{Name: "scope", KnowledgeBaseIDs: []string{"ghost-kb"}}); err == nil {
		t.Fatal("unknown knowledge base scope must be rejected")
	}

	view, token, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{
		Name: "ci", Capabilities: []string{"chat"}, KnowledgeBaseIDs: []string{"kb-a"},
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if !strings.HasPrefix(token, "rk_") || view.ID == "" || view.Name != "ci" {
		t.Fatalf("api key = %+v token = %q", view, token)
	}

	principal, err := service.AuthenticateAPIKey(ctx, token)
	if err != nil {
		t.Fatalf("AuthenticateAPIKey: %v", err)
	}
	if principal.SubjectID != "owner-a" || principal.CredentialType != "api_key" || len(principal.Capabilities) != 1 || principal.Capabilities[0] != "chat" {
		t.Fatalf("api key principal = %+v", principal)
	}
	if len(principal.APIKeyKnowledgeBaseIDs) != 1 || principal.APIKeyKnowledgeBaseIDs[0] != "kb-a" {
		t.Fatalf("api key scope = %+v", principal.APIKeyKnowledgeBaseIDs)
	}

	keys, err := service.ListAPIKeys(ctx, owner)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != view.ID {
		t.Fatalf("api keys = %+v", keys)
	}

	if err := service.RevokeAPIKey(ctx, viewer, view.ID); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer RevokeAPIKey = %v, want forbidden", err)
	}
	if err := service.RevokeAPIKey(ctx, owner, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("unknown RevokeAPIKey = %v, want not found", err)
	}
	if err := service.RevokeAPIKey(ctx, owner, view.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	if _, err := service.AuthenticateAPIKey(ctx, token); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("AuthenticateAPIKey revoked = %v, want unauthenticated", err)
	}
	if _, err := service.AuthenticateAPIKey(ctx, "not-a-key"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("AuthenticateAPIKey malformed = %v, want unauthenticated", err)
	}
	for _, malformed := range []string{"rk_", "rk__secret", "other_prefix_secret"} {
		if _, err := service.AuthenticateAPIKey(ctx, malformed); !errors.Is(err, apperror.ErrUnauthenticated) {
			t.Fatalf("AuthenticateAPIKey(%q) = %v, want unauthenticated", malformed, err)
		}
	}
	parts := strings.SplitN(token, "_", 3)
	if _, err := service.AuthenticateAPIKey(ctx, parts[0]+"_"+parts[1]+"_wrong-secret"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("AuthenticateAPIKey wrong secret = %v, want unauthenticated", err)
	}

	expiring, expiringToken, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{Name: "expiring", ExpiresAt: timePointer(time.Now().Add(time.Hour))})
	if err != nil {
		t.Fatalf("CreateAPIKey expiring: %v", err)
	}
	if err := db.Model(&models.APIKey{}).Where("id = ?", expiring.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire api key: %v", err)
	}
	if _, err := service.AuthenticateAPIKey(ctx, expiringToken); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("AuthenticateAPIKey expired = %v, want unauthenticated", err)
	}

	defaults, _, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{Name: "defaults"})
	if err != nil {
		t.Fatalf("CreateAPIKey defaults: %v", err)
	}
	if len(defaults.Capabilities) != 5 {
		t.Fatalf("default capabilities = %+v", defaults.Capabilities)
	}
	if err := db.Model(&models.APIKey{}).Where("id = ?", defaults.ID).
		Updates(map[string]any{"capabilities": "not-json", "knowledge_base_ids": "not-json"}).Error; err != nil {
		t.Fatalf("corrupt api key scopes: %v", err)
	}
	corrupted, err := service.ListAPIKeys(ctx, owner)
	if err != nil {
		t.Fatalf("ListAPIKeys corrupted: %v", err)
	}
	for _, key := range corrupted {
		if key.ID == defaults.ID && (len(key.Capabilities) != 0 || len(key.KnowledgeBaseIDs) != 0) {
			t.Fatalf("corrupted key scopes = %+v", key)
		}
	}

	orphan, orphanToken, err := service.CreateAPIKey(ctx, owner, CreateAPIKeyInput{Name: "orphan"})
	if err != nil {
		t.Fatalf("CreateAPIKey orphan: %v", err)
	}
	if orphan.ID == "" {
		t.Fatal("orphan key id is empty")
	}
	if err := db.Where("tenant_id = ? AND user_id = ?", "tenant-a", "owner-a").Delete(&models.Membership{}).Error; err != nil {
		t.Fatalf("delete owner membership: %v", err)
	}
	if _, err := service.AuthenticateAPIKey(ctx, orphanToken); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("AuthenticateAPIKey without membership = %v, want unauthenticated", err)
	}
}

func TestResolveDevelopmentPrincipal(t *testing.T) {
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	service := newIdentityAuthService(t, db, nil)
	ctx := context.Background()
	principal, err := service.ResolveDevelopmentPrincipal(ctx, "owner-a", "tenant-a")
	if err != nil {
		t.Fatalf("ResolveDevelopmentPrincipal: %v", err)
	}
	if principal.CredentialType != "dev" || principal.TenantID != "tenant-a" {
		t.Fatalf("principal = %+v", principal)
	}
	if _, err := service.ResolveDevelopmentPrincipal(ctx, "ghost", "tenant-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("unknown principal = %v, want forbidden", err)
	}
}

func TestOIDCBeginAndCompleteWithProvisioning(t *testing.T) {
	provider := newOIDCTestProvider(t, "client-1")
	provider.nonce = "nonce-1"
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	service := newIdentityAuthService(t, db, func(options *AuthOptions) {
		options.OIDC = OIDCOptions{
			Enabled: true, Issuer: provider.server.URL, ClientID: "client-1", ClientSecret: "secret",
			RedirectURL: "http://localhost/callback", Scopes: []string{"openid", "email", "profile"},
			TenantClaim: "tenant_id", EmailClaim: "email", NameClaim: "name",
			AutoProvision: true, DefaultTenant: "tenant-42",
		}
	})
	ctx := context.Background()

	authorizationURL, state, nonce, verifier, err := service.BeginOIDC(ctx)
	if err != nil {
		t.Fatalf("BeginOIDC: %v", err)
	}
	if !strings.HasPrefix(authorizationURL, provider.server.URL+"/authorize?") || state == "" || nonce == "" || verifier == "" {
		t.Fatalf("BeginOIDC = %q state=%q nonce=%q verifier=%q", authorizationURL, state, nonce, verifier)
	}

	result, err := service.CompleteOIDC(ctx, "valid-code", "valid-verifier", "nonce-1", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("CompleteOIDC: %v", err)
	}
	if result.Token == "" || result.User.ID == "" || result.User.Email != "user@example.com" || result.Principal.TenantID != "tenant-42" {
		t.Fatalf("session result = %+v", result)
	}
	var membership models.Membership
	if err := db.First(&membership, "tenant_id = ? AND user_id = ?", "tenant-42", result.User.ID).Error; err != nil {
		t.Fatalf("load oidc membership: %v", err)
	}
	if membership.Role != "member" {
		t.Fatalf("oidc membership role = %q, want member", membership.Role)
	}
	if _, err := service.Authenticate(ctx, result.Token); err != nil {
		t.Fatalf("Authenticate oidc session: %v", err)
	}

	// The second login links to the existing OIDC subject instead of creating a
	// second user.
	again, err := service.CompleteOIDC(ctx, "valid-code", "valid-verifier", "nonce-1", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("second CompleteOIDC: %v", err)
	}
	if again.User.ID != result.User.ID {
		t.Fatalf("second login user = %q, want %q", again.User.ID, result.User.ID)
	}
}

func TestOIDCAutoProvisionWithoutEmailUsesIssuerHost(t *testing.T) {
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	service := newIdentityAuthService(t, db, func(options *AuthOptions) {
		options.OIDC = OIDCOptions{Enabled: true, AutoProvision: true, DefaultTenant: "tenant-42"}
	})
	first, membership, err := service.resolveOIDCUser(context.Background(), OIDCIdentity{
		Issuer: "https://issuer.example", Subject: "sub-no-email", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("resolveOIDCUser without email: %v", err)
	}
	if first.Email != "sub-no-email@issuer.example" {
		t.Fatalf("generated email = %q", first.Email)
	}
	if membership.Role != "member" || membership.TenantID != "tenant-42" {
		t.Fatalf("membership = %+v", membership)
	}
	fallback, _, err := service.resolveOIDCUser(context.Background(), OIDCIdentity{Subject: "sub-no-issuer", EmailVerified: true})
	if err != nil {
		t.Fatalf("resolveOIDCUser without issuer: %v", err)
	}
	if fallback.Email != "sub-no-issuer@oidc.local" {
		t.Fatalf("fallback email = %q", fallback.Email)
	}
}

func TestOIDCClosedRegistrationAndIdentityLinking(t *testing.T) {
	provider := newOIDCTestProvider(t, "client-1")
	provider.nonce = "nonce-1"
	db := openIdentityDB(t)
	seedIdentityDB(t, db)
	closed := newIdentityAuthService(t, db, func(options *AuthOptions) {
		options.OIDC = OIDCOptions{
			Enabled: true, Issuer: provider.server.URL, ClientID: "client-1", ClientSecret: "secret",
			RedirectURL: "http://localhost/callback", Scopes: []string{"openid", "email"},
			TenantClaim: "tenant_id", EmailClaim: "email", NameClaim: "name",
			AutoProvision: false, DefaultTenant: "tenant-42",
		}
	})
	ctx := context.Background()
	if _, err := closed.CompleteOIDC(ctx, "valid-code", "valid-verifier", "nonce-1", "", ""); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("closed registration = %v, want forbidden", err)
	}
	if _, err := closed.CompleteOIDC(ctx, "wrong-code", "valid-verifier", "nonce-1", "", ""); err == nil {
		t.Fatal("rejected authorization code must fail the exchange")
	}

	if err := db.Create(&models.User{ID: "linked", Email: "linked@example.com", DisplayName: "Linked"}).Error; err != nil {
		t.Fatalf("seed linked user: %v", err)
	}
	if err := db.Create(&models.Membership{TenantID: "tenant-42", UserID: "linked", Role: "member"}).Error; err != nil {
		t.Fatalf("seed linked membership: %v", err)
	}
	if _, _, err := closed.resolveOIDCUser(ctx, OIDCIdentity{
		Issuer: "https://issuer.example", Subject: "sub-link", Email: "linked@example.com", EmailVerified: false,
	}); err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("unverified email link = %v, want verification error", err)
	}

	linked, _, err := closed.resolveOIDCUser(ctx, OIDCIdentity{
		Issuer: "https://issuer.example", Subject: "sub-link", Email: "linked@example.com", EmailVerified: true, Name: "Linked User",
	})
	if err != nil {
		t.Fatalf("verified email link: %v", err)
	}
	if linked.ID != "linked" || linked.OIDCIssuer != "https://issuer.example" || linked.OIDCSubject != "sub-link" {
		t.Fatalf("linked user = %+v", linked)
	}
	renamed, _, err := closed.resolveOIDCUser(ctx, OIDCIdentity{
		Issuer: "https://issuer.example", Subject: "sub-link", Email: "linked@example.com", EmailVerified: true, Name: "Renamed User",
	})
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if renamed.DisplayName != "Renamed User" {
		t.Fatalf("display name = %q, want Renamed User", renamed.DisplayName)
	}

	if _, _, err := closed.resolveOIDCUser(ctx, OIDCIdentity{Issuer: "https://issuer.example", Subject: "sub", TenantID: "missing"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("unknown tenant = %v, want forbidden", err)
	}
	noDefault := newIdentityAuthService(t, db, func(options *AuthOptions) {
		options.OIDC = OIDCOptions{Enabled: true, DefaultTenant: ""}
	})
	if _, _, err := noDefault.resolveOIDCUser(ctx, OIDCIdentity{Issuer: "https://issuer.example", Subject: "sub"}); err == nil || !strings.Contains(err.Error(), "tenant mapping") {
		t.Fatalf("missing tenant mapping = %v, want mapping error", err)
	}

	disabled := newIdentityAuthService(t, db, nil)
	if _, _, _, _, err := disabled.BeginOIDC(ctx); err == nil {
		t.Fatal("BeginOIDC must fail when OIDC is disabled")
	}
	if _, err := disabled.CompleteOIDC(ctx, "code", "verifier", "nonce", "", ""); err == nil {
		t.Fatal("CompleteOIDC must fail when OIDC is disabled")
	}
}
