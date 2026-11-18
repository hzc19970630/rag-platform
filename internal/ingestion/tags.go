package ingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

func (s *Service) ListTags(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]models.Tag, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var rows []models.Tag
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("knowledge_base_id = ?", knowledgeBaseID).
		Order("lower(name)").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return rows, nil
}

func (s *Service) CreateTag(ctx context.Context, principal authz.Principal, knowledgeBaseID, name string) (models.Tag, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return models.Tag{}, err
	}
	name, err := validTagName(name)
	if err != nil {
		return models.Tag{}, err
	}
	if err := s.ensureTagNameFree(ctx, principal.TenantID, knowledgeBaseID, name, ""); err != nil {
		return models.Tag{}, err
	}
	id, err := newID("tag")
	if err != nil {
		return models.Tag{}, err
	}
	tag := models.Tag{ID: id, TenantID: principal.TenantID, KnowledgeBaseID: knowledgeBaseID, Name: name}
	if err := s.db.WithContext(ctx).Create(&tag).Error; err != nil {
		if isDuplicateTag(err) {
			return models.Tag{}, apperror.Invalidf("tag name already exists")
		}
		return models.Tag{}, fmt.Errorf("create tag: %w", err)
	}
	if err := s.recordWorkspaceAudit(ctx, principal, "create", "tag", tag.ID, name); err != nil {
		return models.Tag{}, err
	}
	return tag, nil
}

func (s *Service) UpdateTag(ctx context.Context, principal authz.Principal, tagID, name string) (models.Tag, error) {
	tag, err := s.loadTag(ctx, principal.TenantID, tagID)
	if err != nil {
		return models.Tag{}, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, tag.KnowledgeBaseID, "write"); err != nil {
		return models.Tag{}, err
	}
	name, err = validTagName(name)
	if err != nil {
		return models.Tag{}, err
	}
	if err := s.ensureTagNameFree(ctx, principal.TenantID, tag.KnowledgeBaseID, name, tag.ID); err != nil {
		return models.Tag{}, err
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Tag{}).
		Where("id = ? AND knowledge_base_id = ?", tag.ID, tag.KnowledgeBaseID).
		Updates(map[string]any{"name": name, "updated_at": time.Now().UTC()}).Error; err != nil {
		if isDuplicateTag(err) {
			return models.Tag{}, apperror.Invalidf("tag name already exists")
		}
		return models.Tag{}, fmt.Errorf("update tag: %w", err)
	}
	if err := s.recordWorkspaceAudit(ctx, principal, "update", "tag", tag.ID, name); err != nil {
		return models.Tag{}, err
	}
	return s.loadTag(ctx, principal.TenantID, tag.ID)
}

func (s *Service) DeleteTag(ctx context.Context, principal authz.Principal, tagID string) error {
	tag, err := s.loadTag(ctx, principal.TenantID, tagID)
	if err != nil {
		return err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, tag.KnowledgeBaseID, "write"); err != nil {
		return err
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("id = ? AND knowledge_base_id = ?", tag.ID, tag.KnowledgeBaseID).
		Delete(&models.Tag{}).Error; err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	return s.recordWorkspaceAudit(ctx, principal, "delete", "tag", tag.ID, tag.Name)
}

func (s *Service) loadTag(ctx context.Context, tenantID, tagID string) (models.Tag, error) {
	var tag models.Tag
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("id = ?", tagID).First(&tag).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Tag{}, apperror.ErrNotFound
		}
		return models.Tag{}, fmt.Errorf("load tag: %w", err)
	}
	return tag, nil
}

// validateTagIDs rejects ids outside the knowledge base so a forged id cannot
// silently turn a filter into an empty result.
func (s *Service) validateTagIDs(ctx context.Context, tenantID, knowledgeBaseID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	unique := uniqueStrings(ids)
	if len(unique) > maxTagFilters {
		return apperror.Invalidf("too many tag filters")
	}
	var count int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Tag{}).
		Where("knowledge_base_id = ? AND id IN ?", knowledgeBaseID, unique).Count(&count).Error; err != nil {
		return fmt.Errorf("validate tags: %w", err)
	}
	if count != int64(len(unique)) {
		return apperror.ErrNotFound
	}
	return nil
}

func (s *Service) ensureTagNameFree(ctx context.Context, tenantID, knowledgeBaseID, name, excludeID string) error {
	var count int64
	query := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Tag{}).
		Where("knowledge_base_id = ? AND lower(name) = lower(?)", knowledgeBaseID, name)
	if excludeID != "" {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return fmt.Errorf("check tag name: %w", err)
	}
	if count > 0 {
		return apperror.Invalidf("tag name already exists")
	}
	return nil
}

func validTagName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", apperror.Invalidf("tag name is required")
	}
	if utf8.RuneCountInString(name) > maxTagNameSize {
		return "", apperror.Invalidf("tag name is too long")
	}
	return name, nil
}

func isDuplicateTag(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "tags_scope_name")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
