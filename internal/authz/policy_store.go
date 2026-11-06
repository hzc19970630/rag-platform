package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

type PostgresPolicyStore struct {
	db *gorm.DB
}

func NewPostgresPolicyStore(db *gorm.DB) *PostgresPolicyStore {
	return &PostgresPolicyStore{db: db}
}

func (s *PostgresPolicyStore) LoadTenantPolicy(ctx context.Context, tenantID string) (TenantPolicyData, error) {
	var memberships []models.Membership
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Find(&memberships).Error; err != nil {
		return TenantPolicyData{}, fmt.Errorf("load tenant memberships: %w", err)
	}
	var knowledgeBases []models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Find(&knowledgeBases).Error; err != nil {
		return TenantPolicyData{}, fmt.Errorf("load tenant knowledge bases: %w", err)
	}
	kbIDs := make([]string, 0, len(knowledgeBases))
	for _, knowledgeBase := range knowledgeBases {
		kbIDs = append(kbIDs, knowledgeBase.ID)
	}
	var kbMembers []models.KnowledgeBaseMember
	if len(kbIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("knowledge_base_id IN ?", kbIDs).Find(&kbMembers).Error; err != nil {
			return TenantPolicyData{}, fmt.Errorf("load knowledge base memberships: %w", err)
		}
	}
	return TenantPolicyData{Memberships: memberships, KnowledgeBases: knowledgeBases, KnowledgeBaseMembers: kbMembers}, nil
}

func (s *PostgresPolicyStore) CurrentRevision(ctx context.Context, tenantID string) (int64, error) {
	var row models.AuthorizationRevision
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = models.AuthorizationRevision{TenantID: tenantID, Revision: 1}
			if createErr := s.db.WithContext(ctx).Create(&row).Error; createErr != nil {
				var existing models.AuthorizationRevision
				if lookupErr := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).First(&existing).Error; lookupErr == nil {
					return existing.Revision, nil
				}
				return 0, fmt.Errorf("create authorization revision: %w", createErr)
			}
			return row.Revision, nil
		}
		return 0, fmt.Errorf("load authorization revision: %w", err)
	}
	return row.Revision, nil
}

func (s *PostgresPolicyStore) BumpRevision(ctx context.Context, tenantID string) (int64, error) {
	var revision int64
	err := tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		result := tenantdb.MustScoped(tx, tenantID).Model(&models.AuthorizationRevision{}).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": nowUTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			row := models.AuthorizationRevision{TenantID: tenantID, Revision: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			revision = row.Revision
			return nil
		}
		var row models.AuthorizationRevision
		if err := tenantdb.MustScoped(tx, tenantID).First(&row).Error; err != nil {
			return err
		}
		revision = row.Revision
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}
