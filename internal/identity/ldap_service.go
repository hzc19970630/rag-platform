package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// LDAPSyncResult summarizes one directory-to-tenant reconciliation.
type LDAPSyncResult struct {
	Users   int `json:"users"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
}

// LoginLDAP verifies the password with a direct user bind, maps directory
// groups to a tenant role and opens the same session as every other login path.
func (s *Service) LoginLDAP(ctx context.Context, username, password, userAgent, ipAddress string) (SessionResult, error) {
	if !s.LDAPEnabled() {
		return SessionResult{}, apperror.ErrUnauthenticated
	}
	directory, err := s.ldap.Authenticate(ctx, username, password)
	if err != nil {
		return SessionResult{}, err
	}
	role, ok := s.ldap.RoleForGroups(directory.Groups)
	if !ok {
		// Do not distinguish "user exists but has no mapped group" from a bad
		// password at the HTTP boundary.
		return SessionResult{}, apperror.ErrUnauthenticated
	}
	user, membership, err := s.resolveLDAPUser(ctx, directory, role)
	if err != nil {
		return SessionResult{}, err
	}
	return s.createSession(ctx, user, membership, userAgent, ipAddress)
}

// SyncLDAP applies the directory's current users and groups to the configured
// tenant. Users that disappeared from LDAP lose their membership and every
// session for that tenant, so deprovisioning is not delayed by token TTL.
func (s *Service) SyncLDAP(ctx context.Context) (LDAPSyncResult, error) {
	result := LDAPSyncResult{}
	if !s.LDAPEnabled() {
		return result, nil
	}
	users, err := s.ldap.ListUsers(ctx)
	if err != nil {
		return result, err
	}
	result.Users = len(users)
	seen := make([]string, 0, len(users))
	for _, directory := range users {
		role, ok := s.ldap.RoleForGroups(directory.Groups)
		if !ok {
			continue
		}
		user, membership, err := s.resolveLDAPUser(ctx, directory, role)
		if err != nil {
			if errors.Is(err, apperror.ErrForbidden) && !s.cfg.LDAP.AutoProvision {
				// Closed registration: only already-provisioned users are synced.
				continue
			}
			return result, err
		}
		if membership.CreatedAt.Equal(membership.UpdatedAt) {
			result.Created++
		} else {
			result.Updated++
		}
		seen = append(seen, user.ID)
	}
	if len(seen) == 0 {
		return result, nil
	}
	removed, err := s.removeDeprovisionedLDAPUsers(ctx, s.cfg.LDAP.TenantID, seen)
	if err != nil {
		return result, err
	}
	result.Removed = removed
	return result, nil
}

func (s *Service) resolveLDAPUser(ctx context.Context, directory LDAPIdentity, role string) (models.User, models.Membership, error) {
	tenantID := strings.TrimSpace(directory.TenantID)
	if tenantID == "" {
		return models.User{}, models.Membership{}, apperror.ErrForbidden
	}
	var tenant models.Tenant
	if err := s.db.WithContext(ctx).First(&tenant, "id = ?", tenantID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.User{}, models.Membership{}, apperror.ErrForbidden
		}
		return models.User{}, models.Membership{}, fmt.Errorf("load ldap tenant: %w", err)
	}

	var user models.User
	err := s.db.WithContext(ctx).Where("ldap_dn = ?", directory.DN).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
		if directory.Email != "" {
			var byEmail models.User
			if lookupErr := s.db.WithContext(ctx).Where("LOWER(email) = ?", strings.ToLower(directory.Email)).First(&byEmail).Error; lookupErr == nil {
				byEmail.LDAPDN = directory.DN
				byEmail.DisplayName = firstNonEmpty(directory.Name, byEmail.DisplayName)
				if updateErr := s.db.WithContext(ctx).Save(&byEmail).Error; updateErr != nil {
					return models.User{}, models.Membership{}, fmt.Errorf("link ldap identity: %w", updateErr)
				}
				user = byEmail
			} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return models.User{}, models.Membership{}, fmt.Errorf("lookup ldap user by email: %w", lookupErr)
			}
		}
		if user.ID == "" {
			if !s.cfg.LDAP.AutoProvision {
				return models.User{}, models.Membership{}, apperror.ErrForbidden
			}
			id, idErr := newIdentityID("usr")
			if idErr != nil {
				return models.User{}, models.Membership{}, idErr
			}
			email := firstNonEmpty(directory.Email, directory.Username+"@ldap.local")
			user = models.User{
				ID: id, Email: email,
				DisplayName: firstNonEmpty(directory.Name, directory.Username, email),
				LDAPDN:      directory.DN,
			}
			if createErr := s.db.WithContext(ctx).Create(&user).Error; createErr != nil {
				return models.User{}, models.Membership{}, fmt.Errorf("create ldap user: %w", createErr)
			}
		}
	} else if err != nil {
		return models.User{}, models.Membership{}, fmt.Errorf("load ldap user: %w", err)
	} else {
		updates := map[string]any{
			"email":        firstNonEmpty(directory.Email, user.Email),
			"display_name": firstNonEmpty(directory.Name, user.DisplayName),
			"ldap_dn":      directory.DN,
		}
		if err := s.db.WithContext(ctx).Model(&user).Updates(updates).Error; err != nil {
			return models.User{}, models.Membership{}, fmt.Errorf("update ldap user: %w", err)
		}
	}

	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", tenantID, user.ID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.User{}, models.Membership{}, fmt.Errorf("load ldap membership: %w", err)
		}
		if !s.cfg.LDAP.AutoProvision {
			return models.User{}, models.Membership{}, apperror.ErrForbidden
		}
		now := time.Now()
		membership = models.Membership{TenantID: tenantID, UserID: user.ID, Role: role, CreatedAt: now, UpdatedAt: now}
		if createErr := s.db.WithContext(ctx).Create(&membership).Error; createErr != nil {
			return models.User{}, models.Membership{}, fmt.Errorf("create ldap membership: %w", createErr)
		}
		return user, membership, nil
	}
	if membership.Role != role {
		if err := s.db.WithContext(ctx).Model(&membership).Updates(map[string]any{
			"role": role, "updated_at": time.Now(),
		}).Error; err != nil {
			return models.User{}, models.Membership{}, fmt.Errorf("update ldap membership role: %w", err)
		}
		membership.Role = role
	}
	return user, membership, nil
}

func (s *Service) removeDeprovisionedLDAPUsers(ctx context.Context, tenantID string, keepUserIDs []string) (int, error) {
	var userIDs []string
	query := s.db.WithContext(ctx).Table("memberships m").
		Select("m.user_id").
		Joins("JOIN users u ON u.id = m.user_id").
		Where("m.tenant_id = ? AND u.ldap_dn <> ''", tenantID)
	if len(keepUserIDs) > 0 {
		query = query.Where("m.user_id NOT IN ?", keepUserIDs)
	}
	if err := query.Scan(&userIDs).Error; err != nil {
		return 0, fmt.Errorf("list deprovisioned ldap users: %w", err)
	}
	if len(userIDs) == 0 {
		return 0, nil
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.AuthSession{}).
		Where("tenant_id = ? AND user_id IN ? AND revoked_at IS NULL", tenantID, userIDs).
		Updates(map[string]any{"revoked_at": &now, "updated_at": now}).Error; err != nil {
		return 0, fmt.Errorf("revoke deprovisioned ldap sessions: %w", err)
	}
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id IN ?", tenantID, userIDs).
		Delete(&models.Membership{}).Error; err != nil {
		return 0, fmt.Errorf("remove deprovisioned ldap memberships: %w", err)
	}
	return len(userIDs), nil
}
