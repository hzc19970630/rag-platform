package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

type PostgresMembershipStore struct {
	db *gorm.DB
}

func NewPostgresMembershipStore(db *gorm.DB) *PostgresMembershipStore {
	return &PostgresMembershipStore{db: db}
}

func (s *PostgresMembershipStore) List(ctx context.Context, tenantID string) ([]TenantMemberView, error) {
	var rows []struct {
		UserID      string
		Email       string
		DisplayName string
		Role        string
	}
	if err := s.db.WithContext(ctx).Table("memberships m").Select("m.user_id, u.email, u.display_name, m.role").Joins("JOIN users u ON u.id = m.user_id").Where("m.tenant_id = ?", tenantID).Order("m.created_at").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tenant members: %w", err)
	}
	views := make([]TenantMemberView, 0, len(rows))
	for _, row := range rows {
		views = append(views, TenantMemberView{UserID: row.UserID, Email: row.Email, DisplayName: row.DisplayName, Role: row.Role})
	}
	return views, nil
}

func (s *PostgresMembershipStore) FindUser(ctx context.Context, userID string) (models.User, bool, error) {
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.User{}, false, nil
		}
		return models.User{}, false, fmt.Errorf("load user: %w", err)
	}
	return user, true, nil
}

func (s *PostgresMembershipStore) Find(ctx context.Context, tenantID, userID string) (models.Membership, bool, error) {
	var membership models.Membership
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		First(&membership, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Membership{}, false, nil
		}
		return models.Membership{}, false, fmt.Errorf("load tenant membership: %w", err)
	}
	return membership, true, nil
}

func (s *PostgresMembershipStore) Save(ctx context.Context, membership models.Membership) error {
	return s.db.WithContext(ctx).Save(&membership).Error
}

func (s *PostgresMembershipStore) CountOwners(ctx context.Context, tenantID string) (int64, error) {
	var owners int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Membership{}).
		Where("role = ?", "owner").Count(&owners).Error; err != nil {
		return 0, fmt.Errorf("count tenant owners: %w", err)
	}
	return owners, nil
}

func (s *PostgresMembershipStore) UpdateRole(ctx context.Context, tenantID, userID, role string, revokeSessions bool) error {
	if err := tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if err := tx.Model(&models.Membership{}).
			Where("tenant_id = ? AND user_id = ?", tenantID, userID).
			Updates(map[string]any{"role": role, "updated_at": nowUTC()}).Error; err != nil {
			return err
		}
		if !revokeSessions {
			return nil
		}
		return revokeMemberSessions(ctx, tx, tenantID, userID)
	}); err != nil {
		return fmt.Errorf("update tenant member: %w", err)
	}
	return nil
}

func (s *PostgresMembershipStore) Remove(ctx context.Context, tenantID, userID string) error {
	if err := tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND user_id = ?", tenantID, userID).Delete(&models.Membership{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? AND knowledge_base_id IN (SELECT id FROM knowledge_bases WHERE tenant_id = ?)", userID, tenantID).Delete(&models.KnowledgeBaseMember{}).Error; err != nil {
			return err
		}
		return revokeMemberSessions(ctx, tx, tenantID, userID)
	}); err != nil {
		return fmt.Errorf("remove tenant member: %w", err)
	}
	return nil
}

// revokeMemberSessions makes a role change or removal take effect on the next
// request instead of waiting for the JWT/session to expire.
func revokeMemberSessions(ctx context.Context, tx *gorm.DB, tenantID, userID string) error {
	now := nowUTC()
	return tx.WithContext(ctx).Model(&models.AuthSession{}).
		Where("tenant_id = ? AND user_id = ? AND revoked_at IS NULL", tenantID, userID).
		Updates(map[string]any{"revoked_at": &now, "updated_at": now}).Error
}
