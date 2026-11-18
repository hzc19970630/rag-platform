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

const (
	maxFolderDepth    = 16
	maxFolderNameSize = 255
	maxTagNameSize    = 64
)

// FolderUpdate changes a folder name and/or moves it under another folder.
// nil means "keep", an empty ParentID means the root level.
type FolderUpdate struct {
	Name     *string
	ParentID *string
}

func (s *Service) ListFolders(ctx context.Context, principal authz.Principal, knowledgeBaseID string) ([]models.Folder, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return nil, err
	}
	var rows []models.Folder
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("knowledge_base_id = ?", knowledgeBaseID).
		Order("path").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	return rows, nil
}

func (s *Service) CreateFolder(ctx context.Context, principal authz.Principal, knowledgeBaseID, name, parentID string) (models.Folder, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "write"); err != nil {
		return models.Folder{}, err
	}
	name, err := validFolderName(name)
	if err != nil {
		return models.Folder{}, err
	}
	var parent *models.Folder
	if parentID != "" {
		loaded, err := s.loadFolder(ctx, principal.TenantID, knowledgeBaseID, parentID)
		if err != nil {
			return models.Folder{}, err
		}
		parent = &loaded
	}
	depth := 1
	pathPrefix := "/"
	if parent != nil {
		depth = parent.Depth + 1
		pathPrefix = parent.Path
	}
	if depth > maxFolderDepth {
		return models.Folder{}, apperror.Invalidf("folder depth exceeds %d", maxFolderDepth)
	}
	if err := s.ensureSiblingNameFree(ctx, principal.TenantID, knowledgeBaseID, parentID, name, ""); err != nil {
		return models.Folder{}, err
	}
	id, err := newID("fld")
	if err != nil {
		return models.Folder{}, err
	}
	folder := models.Folder{
		ID: id, TenantID: principal.TenantID, KnowledgeBaseID: knowledgeBaseID,
		ParentID: nullableString(parentID), Name: name, Path: pathPrefix + id + "/", Depth: depth,
	}
	if err := s.db.WithContext(ctx).Create(&folder).Error; err != nil {
		if isDuplicateSibling(err) {
			return models.Folder{}, apperror.Invalidf("folder name already exists")
		}
		return models.Folder{}, fmt.Errorf("create folder: %w", err)
	}
	if err := s.recordWorkspaceAudit(ctx, principal, "create", "folder", folder.ID, name); err != nil {
		return models.Folder{}, err
	}
	return folder, nil
}

func (s *Service) UpdateFolder(ctx context.Context, principal authz.Principal, folderID string, input FolderUpdate) (models.Folder, error) {
	folder, err := s.loadFolderByID(ctx, principal.TenantID, folderID)
	if err != nil {
		return models.Folder{}, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, folder.KnowledgeBaseID, "write"); err != nil {
		return models.Folder{}, err
	}
	name := folder.Name
	if input.Name != nil {
		name, err = validFolderName(*input.Name)
		if err != nil {
			return models.Folder{}, err
		}
	}
	currentParent := deref(folder.ParentID)
	newParent := currentParent
	if input.ParentID != nil {
		newParent = strings.TrimSpace(*input.ParentID)
	}
	var parent *models.Folder
	if newParent != "" {
		if newParent == folder.ID {
			return models.Folder{}, apperror.Invalidf("folder cannot be its own parent")
		}
		loaded, err := s.loadFolder(ctx, principal.TenantID, folder.KnowledgeBaseID, newParent)
		if err != nil {
			return models.Folder{}, err
		}
		if strings.HasPrefix(loaded.Path, folder.Path) {
			return models.Folder{}, apperror.Invalidf("folder cannot move into its subtree")
		}
		parent = &loaded
	}
	if err := s.ensureSiblingNameFree(ctx, principal.TenantID, folder.KnowledgeBaseID, newParent, name, folder.ID); err != nil {
		return models.Folder{}, err
	}
	moving := newParent != currentParent
	newPath := folder.Path
	newDepth := folder.Depth
	delta := 0
	if moving {
		pathPrefix := "/"
		if parent != nil {
			pathPrefix = parent.Path
		}
		newPath = pathPrefix + folder.ID + "/"
		newDepth = 1
		if parent != nil {
			newDepth = parent.Depth + 1
		}
		delta = newDepth - folder.Depth
		var deepest int
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Folder{}).
			Where("knowledge_base_id = ? AND path LIKE ?", folder.KnowledgeBaseID, folder.Path+"%").
			Select("COALESCE(MAX(depth), 0)").Scan(&deepest).Error; err != nil {
			return models.Folder{}, fmt.Errorf("load folder subtree depth: %w", err)
		}
		if deepest+delta > maxFolderDepth {
			return models.Folder{}, apperror.Invalidf("folder depth exceeds %d", maxFolderDepth)
		}
	}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if moving {
			var subtree []models.Folder
			if err := tenantdb.MustScoped(tx, principal.TenantID).
				Where("knowledge_base_id = ? AND path LIKE ?", folder.KnowledgeBaseID, folder.Path+"%").
				Find(&subtree).Error; err != nil {
				return fmt.Errorf("load folder subtree: %w", err)
			}
			for _, node := range subtree {
				suffix := strings.TrimPrefix(node.Path, folder.Path)
				if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Folder{}).
					Where("id = ?", node.ID).
					Updates(map[string]any{"path": newPath + suffix, "depth": node.Depth + delta, "updated_at": time.Now().UTC()}).Error; err != nil {
					return fmt.Errorf("move folder subtree: %w", err)
				}
			}
		}
		updates := map[string]any{"name": name, "updated_at": time.Now().UTC()}
		if moving {
			updates["parent_id"] = nullableString(newParent)
			updates["path"] = newPath
			updates["depth"] = newDepth
		}
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Folder{}).
			Where("id = ? AND knowledge_base_id = ?", folder.ID, folder.KnowledgeBaseID).
			Updates(updates).Error; err != nil {
			if isDuplicateSibling(err) {
				return apperror.Invalidf("folder name already exists")
			}
			return fmt.Errorf("update folder: %w", err)
		}
		return nil
	})
	if err != nil {
		return models.Folder{}, err
	}
	return s.loadFolderByID(ctx, principal.TenantID, folder.ID)
}

func (s *Service) DeleteFolder(ctx context.Context, principal authz.Principal, folderID string) error {
	folder, err := s.loadFolderByID(ctx, principal.TenantID, folderID)
	if err != nil {
		return err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, folder.KnowledgeBaseID, "write"); err != nil {
		return err
	}
	var children int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Folder{}).
		Where("knowledge_base_id = ? AND parent_id = ?", folder.KnowledgeBaseID, folder.ID).Count(&children).Error; err != nil {
		return fmt.Errorf("count child folders: %w", err)
	}
	var documents int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Document{}).
		Where("knowledge_base_id = ? AND folder_id = ? AND deleted_at IS NULL", folder.KnowledgeBaseID, folder.ID).Count(&documents).Error; err != nil {
		return fmt.Errorf("count folder documents: %w", err)
	}
	if children > 0 || documents > 0 {
		return apperror.Invalidf("folder is not empty")
	}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		// Soft-deleted documents still point at the folder for restore. They no
		// longer make it "non-empty"; detach them so restore lands at the root.
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).
			Where("knowledge_base_id = ? AND folder_id = ? AND deleted_at IS NOT NULL", folder.KnowledgeBaseID, folder.ID).
			Update("folder_id", nil).Error; err != nil {
			return fmt.Errorf("detach deleted folder documents: %w", err)
		}
		if err := tenantdb.MustScoped(tx, principal.TenantID).
			Where("id = ? AND knowledge_base_id = ?", folder.ID, folder.KnowledgeBaseID).
			Delete(&models.Folder{}).Error; err != nil {
			return fmt.Errorf("delete folder: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.recordWorkspaceAudit(ctx, principal, "delete", "folder", folder.ID, folder.Name)
}

func (s *Service) loadFolder(ctx context.Context, tenantID, knowledgeBaseID, folderID string) (models.Folder, error) {
	var folder models.Folder
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("knowledge_base_id = ? AND id = ?", knowledgeBaseID, folderID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Folder{}, apperror.ErrNotFound
		}
		return models.Folder{}, fmt.Errorf("load folder: %w", err)
	}
	return folder, nil
}

func (s *Service) loadFolderByID(ctx context.Context, tenantID, folderID string) (models.Folder, error) {
	var folder models.Folder
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("id = ?", folderID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Folder{}, apperror.ErrNotFound
		}
		return models.Folder{}, fmt.Errorf("load folder: %w", err)
	}
	return folder, nil
}

func (s *Service) ensureSiblingNameFree(ctx context.Context, tenantID, knowledgeBaseID, parentID, name, excludeID string) error {
	var count int64
	query := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Folder{}).
		Where("knowledge_base_id = ? AND COALESCE(parent_id, '') = ? AND lower(name) = lower(?)", knowledgeBaseID, parentID, name)
	if excludeID != "" {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return fmt.Errorf("check sibling folder name: %w", err)
	}
	if count > 0 {
		return apperror.Invalidf("folder name already exists")
	}
	return nil
}

func validFolderName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", apperror.Invalidf("folder name is required")
	}
	if utf8.RuneCountInString(name) > maxFolderNameSize {
		return "", apperror.Invalidf("folder name is too long")
	}
	return name, nil
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isDuplicateSibling(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "folders_sibling_name")
}

// recordWorkspaceAudit writes folder/tag state changes; document mutations in a
// batch are audited separately per document.
func (s *Service) recordWorkspaceAudit(ctx context.Context, principal authz.Principal, action, resourceType, resourceID, reason string) error {
	if err := s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Allowed: true, Reason: reason,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error; err != nil {
		return fmt.Errorf("record %s %s audit: %w", action, resourceType, err)
	}
	return nil
}
