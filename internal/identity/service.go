package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const (
	DefaultDevUserID   = "dev-user"
	DefaultDevTenantID = "dev-tenant"
)

type Service struct {
	db     *gorm.DB
	cfg    config.AuthConfig
	signer *tokenSigner
	oidc   *OIDCClient
}

type SessionResult struct {
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expiresAt"`
	User      models.User     `json:"user"`
	Principal authz.Principal `json:"principal"`
}

type CreateAPIKeyInput struct {
	Name             string     `json:"name"`
	Capabilities     []string   `json:"capabilities"`
	KnowledgeBaseIDs []string   `json:"knowledgeBaseIds"`
	ExpiresAt        *time.Time `json:"expiresAt"`
}

type APIKeyView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	KeyPrefix        string     `json:"keyPrefix"`
	Capabilities     []string   `json:"capabilities"`
	KnowledgeBaseIDs []string   `json:"knowledgeBaseIds"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt       *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func NewService(db *gorm.DB, cfg config.AuthConfig) (*Service, error) {
	signer, err := newTokenSigner(cfg)
	if err != nil {
		return nil, err
	}
	service := &Service{db: db, cfg: cfg, signer: signer}
	if cfg.OIDC.Enabled {
		service.oidc = NewOIDCClient(cfg.OIDC)
	}
	return service, nil
}

func (s *Service) OIDCEnabled() bool { return s.oidc != nil && s.oidc.Enabled() }

func (s *Service) OIDCIssuer() string { return s.cfg.OIDC.Issuer }

// BeginOIDC creates the state, nonce and PKCE verifier for one login attempt.
// The caller stores them in short-lived HttpOnly cookies and verifies that the
// callback state matches before exchanging the code.
func (s *Service) BeginOIDC(ctx context.Context) (authorizationURL, state, nonce, verifier string, err error) {
	if !s.OIDCEnabled() {
		return "", "", "", "", fmt.Errorf("OIDC is not enabled")
	}
	state, err = randomToken(24)
	if err != nil {
		return "", "", "", "", err
	}
	nonce, err = randomToken(24)
	if err != nil {
		return "", "", "", "", err
	}
	verifier, err = randomToken(48)
	if err != nil {
		return "", "", "", "", err
	}
	authorizationURL, err = s.oidc.AuthorizationURL(ctx, state, nonce, verifier)
	if err != nil {
		return "", "", "", "", err
	}
	return authorizationURL, state, nonce, verifier, nil
}

func (s *Service) CompleteOIDC(ctx context.Context, code, verifier, nonce, userAgent, ipAddress string) (SessionResult, error) {
	if !s.OIDCEnabled() {
		return SessionResult{}, fmt.Errorf("OIDC is not enabled")
	}
	identity, err := s.oidc.Exchange(ctx, code, verifier, nonce)
	if err != nil {
		return SessionResult{}, err
	}
	user, membership, err := s.resolveOIDCUser(ctx, identity)
	if err != nil {
		return SessionResult{}, err
	}
	return s.createSession(ctx, user, membership, userAgent, ipAddress)
}

// resolveOIDCUser is the tenant mapping and closed-registration policy. A user
// is only linked by email when the provider says the email is verified, and a
// membership is never invented unless OIDC_AUTO_PROVISION is explicitly on.
func (s *Service) resolveOIDCUser(ctx context.Context, identity OIDCIdentity) (models.User, models.Membership, error) {
	tenantID := strings.TrimSpace(identity.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(s.cfg.OIDC.DefaultTenant)
	}
	if tenantID == "" {
		return models.User{}, models.Membership{}, fmt.Errorf("OIDC identity did not provide a tenant mapping")
	}
	var tenant models.Tenant
	if err := s.db.WithContext(ctx).First(&tenant, "id = ?", tenantID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.User{}, models.Membership{}, apperror.ErrForbidden
		}
		return models.User{}, models.Membership{}, fmt.Errorf("load oidc tenant: %w", err)
	}

	var user models.User
	err := s.db.WithContext(ctx).Where("oidc_issuer = ? AND oidc_subject = ?", identity.Issuer, identity.Subject).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
		if identity.Email != "" {
			var byEmail models.User
			if lookupErr := s.db.WithContext(ctx).Where("LOWER(email) = ?", strings.ToLower(identity.Email)).First(&byEmail).Error; lookupErr == nil {
				if !identity.EmailVerified {
					return models.User{}, models.Membership{}, fmt.Errorf("email is not verified by the OIDC provider")
				}
				byEmail.OIDCIssuer = identity.Issuer
				byEmail.OIDCSubject = identity.Subject
				byEmail.DisplayName = firstNonEmpty(identity.Name, byEmail.DisplayName)
				if updateErr := s.db.WithContext(ctx).Save(&byEmail).Error; updateErr != nil {
					return models.User{}, models.Membership{}, fmt.Errorf("link oidc identity: %w", updateErr)
				}
				user = byEmail
			} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return models.User{}, models.Membership{}, fmt.Errorf("lookup user by email: %w", lookupErr)
			}
		}
		if user.ID == "" {
			if !s.cfg.OIDC.AutoProvision {
				return models.User{}, models.Membership{}, apperror.ErrForbidden
			}
			id, idErr := newIdentityID("usr")
			if idErr != nil {
				return models.User{}, models.Membership{}, idErr
			}
			email := identity.Email
			if email == "" {
				email = identity.Subject + "@" + hostOf(identity.Issuer)
			}
			user = models.User{ID: id, Email: email, DisplayName: firstNonEmpty(identity.Name, email), OIDCIssuer: identity.Issuer, OIDCSubject: identity.Subject}
			if createErr := s.db.WithContext(ctx).Create(&user).Error; createErr != nil {
				return models.User{}, models.Membership{}, fmt.Errorf("create oidc user: %w", createErr)
			}
		}
	} else if err != nil {
		return models.User{}, models.Membership{}, fmt.Errorf("load oidc user: %w", err)
	} else {
		updates := map[string]any{"email": firstNonEmpty(identity.Email, user.Email), "display_name": firstNonEmpty(identity.Name, user.DisplayName)}
		if err := s.db.WithContext(ctx).Model(&user).Updates(updates).Error; err != nil {
			return models.User{}, models.Membership{}, fmt.Errorf("update oidc user: %w", err)
		}
	}

	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", tenantID, user.ID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.User{}, models.Membership{}, fmt.Errorf("load oidc membership: %w", err)
		}
		if !s.cfg.OIDC.AutoProvision {
			return models.User{}, models.Membership{}, apperror.ErrForbidden
		}
		membership = models.Membership{TenantID: tenantID, UserID: user.ID, Role: "member"}
		if err := s.db.WithContext(ctx).Create(&membership).Error; err != nil {
			return models.User{}, models.Membership{}, fmt.Errorf("create oidc membership: %w", err)
		}
	}
	return user, membership, nil
}

func (s *Service) LoginDevelopment(ctx context.Context, userID, tenantID, userAgent, ipAddress string) (SessionResult, error) {
	if !s.cfg.DevAuthEnabled {
		return SessionResult{}, apperror.ErrUnauthenticated
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", tenantID, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SessionResult{}, apperror.ErrForbidden
		}
		return SessionResult{}, fmt.Errorf("load development membership: %w", err)
	}
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return SessionResult{}, fmt.Errorf("load development user: %w", err)
	}
	return s.createSession(ctx, user, membership, userAgent, ipAddress)
}

func (s *Service) createSession(ctx context.Context, user models.User, membership models.Membership, userAgent, ipAddress string) (SessionResult, error) {
	now := time.Now()
	expiresAt := now.Add(s.signer.ttl)
	sessionID, err := newIdentityID("ses")
	if err != nil {
		return SessionResult{}, err
	}
	tokenID, err := newIdentityID("tok")
	if err != nil {
		return SessionResult{}, err
	}
	token, err := s.signer.issue(sessionClaims{Subject: user.ID, TenantID: membership.TenantID, SessionID: sessionID, TokenID: tokenID, ExpiresAt: expiresAt, IssuedAt: now})
	if err != nil {
		return SessionResult{}, err
	}
	session := models.AuthSession{ID: sessionID, UserID: user.ID, TenantID: membership.TenantID, TokenID: tokenID, ExpiresAt: expiresAt, LastSeenAt: now, UserAgent: truncate(userAgent, 512), IPAddress: truncate(ipAddress, 128)}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return SessionResult{}, fmt.Errorf("create auth session: %w", err)
	}
	if err := s.db.WithContext(ctx).Create(&models.AuditLog{TenantID: membership.TenantID, ActorID: user.ID, Action: "login", ResourceType: "session", ResourceID: sessionID, Allowed: true, Reason: "credential_type=session", IPAddress: truncate(ipAddress, 128), UserAgent: truncate(userAgent, 512)}).Error; err != nil {
		return SessionResult{}, fmt.Errorf("record login audit: %w", err)
	}
	return SessionResult{Token: token, ExpiresAt: expiresAt, User: user, Principal: authz.Principal{SubjectID: user.ID, TenantID: membership.TenantID, CredentialType: "session", CredentialID: sessionID}}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (authz.Principal, error) {
	claims, err := s.signer.verify(rawToken)
	if err != nil {
		return authz.Principal{}, apperror.ErrUnauthenticated
	}
	var session models.AuthSession
	if err := s.db.WithContext(ctx).Where("token_id = ? AND revoked_at IS NULL", claims.TokenID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return authz.Principal{}, apperror.ErrUnauthenticated
		}
		return authz.Principal{}, fmt.Errorf("load auth session: %w", err)
	}
	if !session.ExpiresAt.After(time.Now()) || session.UserID != claims.Subject || session.TenantID != claims.TenantID {
		return authz.Principal{}, apperror.ErrUnauthenticated
	}
	_ = s.db.WithContext(ctx).Model(&session).Updates(map[string]any{"last_seen_at": time.Now(), "updated_at": time.Now()}).Error
	return authz.Principal{SubjectID: session.UserID, TenantID: session.TenantID, CredentialType: "session", CredentialID: session.ID}, nil
}

func (s *Service) Refresh(ctx context.Context, rawToken string) (string, time.Time, error) {
	claims, err := s.signer.verify(rawToken)
	if err != nil {
		return "", time.Time{}, apperror.ErrUnauthenticated
	}
	var session models.AuthSession
	if err := s.db.WithContext(ctx).Where("token_id = ? AND revoked_at IS NULL", claims.TokenID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", time.Time{}, apperror.ErrUnauthenticated
		}
		return "", time.Time{}, fmt.Errorf("load auth session: %w", err)
	}
	if !session.ExpiresAt.After(time.Now()) {
		return "", time.Time{}, apperror.ErrUnauthenticated
	}
	// Re-sign with the currently active key while keeping the same server-side
	// session and expiry. This is what makes key rotation observable.
	token, err := s.signer.issue(sessionClaims{Subject: session.UserID, TenantID: session.TenantID, SessionID: session.ID, TokenID: session.TokenID, ExpiresAt: session.ExpiresAt, IssuedAt: time.Now()})
	if err != nil {
		return "", time.Time{}, err
	}
	if err := s.db.WithContext(ctx).Model(&session).Updates(map[string]any{"last_seen_at": time.Now(), "updated_at": time.Now()}).Error; err != nil {
		return "", time.Time{}, fmt.Errorf("refresh auth session: %w", err)
	}
	return token, session.ExpiresAt, nil
}

func (s *Service) RevokeSession(ctx context.Context, rawToken string) error {
	claims, err := s.signer.verify(rawToken)
	if err != nil {
		return apperror.ErrUnauthenticated
	}
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.AuthSession{}).Where("token_id = ? AND revoked_at IS NULL", claims.TokenID).Updates(map[string]any{"revoked_at": &now, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("revoke auth session: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.ErrUnauthenticated
	}
	return nil
}

func (s *Service) CreateAPIKey(ctx context.Context, principal authz.Principal, input CreateAPIKeyInput) (APIKeyView, string, error) {
	if strings.TrimSpace(input.Name) == "" {
		return APIKeyView{}, "", fmt.Errorf("name is required")
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Error; err != nil {
		return APIKeyView{}, "", apperror.ErrForbidden
	}
	if membership.Role != "owner" && membership.Role != "admin" {
		return APIKeyView{}, "", apperror.ErrForbidden
	}
	capabilities := input.Capabilities
	if len(capabilities) == 0 {
		capabilities = []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members", "chat"}
	}
	for _, capability := range capabilities {
		if !authz.KnownCapability(capability) {
			return APIKeyView{}, "", fmt.Errorf("unsupported capability %q", capability)
		}
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return APIKeyView{}, "", fmt.Errorf("expiresAt must be in the future")
	}
	if len(input.KnowledgeBaseIDs) > 0 {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.KnowledgeBase{}).Where("tenant_id = ? AND id IN ?", principal.TenantID, input.KnowledgeBaseIDs).Count(&count).Error; err != nil {
			return APIKeyView{}, "", fmt.Errorf("validate knowledge base scope: %w", err)
		}
		if int(count) != len(input.KnowledgeBaseIDs) {
			return APIKeyView{}, "", fmt.Errorf("knowledge base scope contains an unsupported knowledge base")
		}
	}
	prefix, secret, err := generateAPIKey()
	if err != nil {
		return APIKeyView{}, "", err
	}
	id, err := newIdentityID("key")
	if err != nil {
		return APIKeyView{}, "", err
	}
	capabilitiesJSON, _ := json.Marshal(capabilities)
	knowledgeBasesJSON, _ := json.Marshal(input.KnowledgeBaseIDs)
	row := models.APIKey{ID: id, TenantID: principal.TenantID, CreatedBy: principal.SubjectID, Name: input.Name, KeyPrefix: prefix, KeyHash: s.hashAPIKey(prefix + "_" + secret), Capabilities: string(capabilitiesJSON), KnowledgeBaseIDs: string(knowledgeBasesJSON), ExpiresAt: input.ExpiresAt}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return APIKeyView{}, "", fmt.Errorf("create api key: %w", err)
	}
	if err := s.db.WithContext(ctx).Create(&models.AuditLog{TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "create", ResourceType: "api_key", ResourceID: id, Allowed: true, Reason: "capabilities=" + strings.Join(capabilities, ","), RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent}).Error; err != nil {
		return APIKeyView{}, "", fmt.Errorf("record api key audit: %w", err)
	}
	return apiKeyView(row), prefix + "_" + secret, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, principal authz.Principal) ([]APIKeyView, error) {
	var rows []models.APIKey
	if err := s.db.WithContext(ctx).Where("tenant_id = ?", principal.TenantID).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	views := make([]APIKeyView, 0, len(rows))
	for _, row := range rows {
		views = append(views, apiKeyView(row))
	}
	return views, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, principal authz.Principal, id string) error {
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", principal.TenantID, principal.SubjectID).Error; err != nil {
		return apperror.ErrForbidden
	}
	if membership.Role != "owner" && membership.Role != "admin" {
		return apperror.ErrForbidden
	}
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.APIKey{}).Where("tenant_id = ? AND id = ? AND revoked_at IS NULL", principal.TenantID, id).Updates(map[string]any{"revoked_at": &now, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("revoke api key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.ErrNotFound
	}
	return s.db.WithContext(ctx).Create(&models.AuditLog{TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "revoke", ResourceType: "api_key", ResourceID: id, Allowed: true, RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent}).Error
}

func (s *Service) AuthenticateAPIKey(ctx context.Context, rawKey string) (authz.Principal, error) {
	prefix, ok := apiKeyPrefix(rawKey)
	if !ok {
		return authz.Principal{}, apperror.ErrUnauthenticated
	}
	var row models.APIKey
	if err := s.db.WithContext(ctx).Where("key_prefix = ?", prefix).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return authz.Principal{}, apperror.ErrUnauthenticated
		}
		return authz.Principal{}, fmt.Errorf("load api key: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(row.KeyHash), []byte(s.hashAPIKey(rawKey))) != 1 {
		return authz.Principal{}, apperror.ErrUnauthenticated
	}
	if row.RevokedAt != nil || (row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now())) {
		return authz.Principal{}, apperror.ErrUnauthenticated
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", row.TenantID, row.CreatedBy).Error; err != nil {
		return authz.Principal{}, apperror.ErrUnauthenticated
	}
	now := time.Now()
	_ = s.db.WithContext(ctx).Model(&row).Updates(map[string]any{"last_used_at": &now, "updated_at": now}).Error
	return authz.Principal{
		SubjectID: row.CreatedBy, TenantID: row.TenantID,
		Capabilities:           decodeStringArray(row.Capabilities),
		CredentialType:         "api_key",
		CredentialID:           row.ID,
		APIKeyKnowledgeBaseIDs: decodeStringArray(row.KnowledgeBaseIDs),
	}, nil
}

func (s *Service) ResolveDevelopmentPrincipal(ctx context.Context, userID, tenantID string) (authz.Principal, error) {
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", tenantID, userID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return authz.Principal{}, apperror.ErrForbidden
		}
		return authz.Principal{}, fmt.Errorf("resolve development identity: %w", err)
	}
	return authz.Principal{SubjectID: userID, TenantID: tenantID, CredentialType: "dev"}, nil
}

func (s *Service) hashAPIKey(raw string) string {
	digest := sha256.Sum256([]byte(s.cfg.APIKeyPepper + raw))
	return hex.EncodeToString(digest[:])
}

func apiKeyView(row models.APIKey) APIKeyView {
	return APIKeyView{
		ID: row.ID, Name: row.Name, KeyPrefix: row.KeyPrefix,
		Capabilities: decodeStringArray(row.Capabilities), KnowledgeBaseIDs: decodeStringArray(row.KnowledgeBaseIDs),
		ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt, LastUsedAt: row.LastUsedAt, CreatedAt: row.CreatedAt,
	}
}

func generateAPIKey() (prefix, secret string, err error) {
	prefixBytes := make([]byte, 8)
	if _, err := rand.Read(prefixBytes); err != nil {
		return "", "", fmt.Errorf("generate api key prefix: %w", err)
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", fmt.Errorf("generate api key secret: %w", err)
	}
	return "rk_" + hex.EncodeToString(prefixBytes), base64.RawURLEncoding.EncodeToString(secretBytes), nil
}

func apiKeyPrefix(raw string) (string, bool) {
	parts := strings.SplitN(raw, "_", 3)
	if len(parts) != 3 || parts[0] != "rk" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return "rk_" + parts[1], true
}

func decodeStringArray(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return []string{}
	}
	if values == nil {
		return []string{}
	}
	return values
}

func newIdentityID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hostOf(raw string) string {
	value := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if index := strings.IndexByte(value, '/'); index >= 0 {
		value = value[:index]
	}
	if value == "" {
		return "oidc.local"
	}
	return value
}
