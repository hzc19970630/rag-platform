package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

type PostgresScopeStore struct {
	db *gorm.DB
}

func NewPostgresScopeStore(db *gorm.DB) *PostgresScopeStore {
	return &PostgresScopeStore{db: db}
}

func (s *PostgresScopeStore) Subject(ctx context.Context, tenantID, userID string) (ScopeSubject, bool, error) {
	var membership models.Membership
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		First(&membership, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ScopeSubject{}, false, nil
		}
		return ScopeSubject{}, false, fmt.Errorf("load tenant membership: %w", err)
	}
	ids := make([]string, 0, 8)
	if membership.Role == "owner" || membership.Role == "admin" {
		var knowledgeBases []models.KnowledgeBase
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Find(&knowledgeBases).Error; err != nil {
			return ScopeSubject{}, false, fmt.Errorf("list tenant knowledge bases: %w", err)
		}
		for _, knowledgeBase := range knowledgeBases {
			ids = append(ids, knowledgeBase.ID)
		}
	} else {
		var rows []struct{ KnowledgeBaseID string }
		if err := s.db.WithContext(ctx).Table("knowledge_base_members kb_member").Select("kb_member.knowledge_base_id").Joins("JOIN knowledge_bases kb ON kb.id = kb_member.knowledge_base_id").Where("kb.tenant_id = ? AND kb_member.user_id = ?", tenantID, userID).Scan(&rows).Error; err != nil {
			return ScopeSubject{}, false, fmt.Errorf("list member knowledge bases: %w", err)
		}
		for _, row := range rows {
			ids = append(ids, row.KnowledgeBaseID)
		}
	}
	return ScopeSubject{Role: membership.Role, KnowledgeBaseIDs: ids}, true, nil
}
