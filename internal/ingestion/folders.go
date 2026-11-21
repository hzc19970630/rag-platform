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
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
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
	newParent := deref(folder.ParentID)
	if input.ParentID != nil {
		newParent = strings.TrimSpace(*input.ParentID)
	}
	// Every validation below re-runs inside the transaction: the KB lock makes
	// the cycle/depth checks and the subtree rewrite one serial unit.
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := persistence.LockKnowledgeBaseXact(tx, principal.TenantID, folder.KnowledgeBaseID); err != nil {
			return err
		}
		current, err := s.loadFolderByIDOn(ctx, tx, principal.TenantID, folderID)
		if err != nil {
			return err
		}
		var parent *models.Folder
		if newParent != "" {
			if newParent == current.ID {
				return apperror.Invalidf("folder cannot be its own parent")
			}
			loaded, err := s.loadFolderOn(ctx, tx, principal.TenantID, current.KnowledgeBaseID, newParent)
			if err != nil {
				return err
			}
			if strings.HasPrefix(loaded.Path, current.Path) {
				return apperror.Invalidf("folder cannot move into its subtree")
			}
			parent = &loaded
		}
		if err := s.ensureSiblingNameFreeOn(ctx, tx, principal.TenantID, current.KnowledgeBaseID, newParent, name, current.ID); err != nil {
			return err
		}
		moving := newParent != deref(current.ParentID)
		newPath := current.Path
		newDepth := current.Depth
		delta := 0
		if moving {
			pathPrefix := "/"
			if parent != nil {
				pathPrefix = parent.Path
			}
			newPath = pathPrefix + current.ID + "/"
			newDepth = 1
			if parent != nil {
				newDepth = parent.Depth + 1
			}
			delta = newDepth - current.Depth
			var deepest int
			if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Folder{}).
				Where("knowledge_base_id = ? AND path LIKE ?", current.KnowledgeBaseID, current.Path+"%").
				Select("COALESCE(MAX(depth), 0)").Scan(&deepest).Error; err != nil {
				return fmt.Errorf("load folder subtree depth: %w", err)
			}
			if deepest+delta > maxFolderDepth {
				return apperror.Invalidf("folder depth exceeds %d", maxFolderDepth)
			}
			var subtree []models.Folder
			if err := tenantdb.MustScoped(tx, principal.TenantID).
				Where("knowledge_base_id = ? AND path LIKE ?", current.KnowledgeBaseID, current.Path+"%").
				Find(&subtree).Error; err != nil {
				return fmt.Errorf("load folder subtree: %w", err)
			}
			for _, node := range subtree {
				suffix := strings.TrimPrefix(node.Path, current.Path)
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
			Where("id = ? AND knowledge_base_id = ?", current.ID, current.KnowledgeBaseID).
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

func (s *Service) DeleteFolder(ctx context.Context, principal authz.Principal, folderID string, recursive bool) error {
	folder, err := s.loadFolderByID(ctx, principal.TenantID, folderID)
	if err != nil {
		return err
	}
	action := "write"
	if recursive {
		// Deleting documents is a delete-level action; a recursive folder delete
		// must not be reachable with write-only permission.
		action = "delete"
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, folder.KnowledgeBaseID, action); err != nil {
		return err
	}
	if recursive {
		return s.deleteFolderTree(ctx, principal, folder)
	}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := persistence.LockKnowledgeBaseXact(tx, principal.TenantID, folder.KnowledgeBaseID); err != nil {
			return err
		}
		current, err := s.loadFolderByIDOn(ctx, tx, principal.TenantID, folder.ID)
		if err != nil {
			return err
		}
		var children int64
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Folder{}).
			Where("knowledge_base_id = ? AND parent_id = ?", current.KnowledgeBaseID, current.ID).Count(&children).Error; err != nil {
			return fmt.Errorf("count child folders: %w", err)
		}
		var documents int64
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).
			Where("knowledge_base_id = ? AND folder_id = ? AND deleted_at IS NULL", current.KnowledgeBaseID, current.ID).Count(&documents).Error; err != nil {
			return fmt.Errorf("count folder documents: %w", err)
		}
		if children > 0 || documents > 0 {
			return apperror.Invalidf("folder is not empty")
		}
		// Soft-deleted documents still point at the folder for restore. They no
		// longer make it "non-empty"; detach them so restore lands at the root.
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).
			Where("knowledge_base_id = ? AND folder_id = ? AND deleted_at IS NOT NULL", current.KnowledgeBaseID, current.ID).
			Update("folder_id", nil).Error; err != nil {
			return fmt.Errorf("detach deleted folder documents: %w", err)
		}
		if err := tenantdb.MustScoped(tx, principal.TenantID).
			Where("id = ? AND knowledge_base_id = ?", current.ID, current.KnowledgeBaseID).
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

// deleteFolderTree removes one folder subtree and soft-deletes every live
// document inside it. Folders themselves have no recycle bin; documents keep
// their retention window and are detached so a restore lands at the root.
func (s *Service) deleteFolderTree(ctx context.Context, principal authz.Principal, folder models.Folder) error {
	// The subtree read, document soft-deletes and folder deletes share one
	// transaction under the KB lock, so a concurrent move cannot land between
	// the read and the delete.
	var deletedDocuments []models.Document
	err := tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := persistence.LockKnowledgeBaseXact(tx, principal.TenantID, folder.KnowledgeBaseID); err != nil {
			return err
		}
		current, err := s.loadFolderByIDOn(ctx, tx, principal.TenantID, folder.ID)
		if err != nil {
			return err
		}
		var subtree []models.Folder
		if err := tenantdb.MustScoped(tx, principal.TenantID).
			Where("knowledge_base_id = ? AND path LIKE ?", current.KnowledgeBaseID, current.Path+"%").
			Order("depth DESC").Find(&subtree).Error; err != nil {
			return fmt.Errorf("load folder subtree: %w", err)
		}
		folderIDs := make([]string, 0, len(subtree))
		for _, node := range subtree {
			folderIDs = append(folderIDs, node.ID)
		}
		var documents []models.Document
		if err := tenantdb.MustScoped(tx, principal.TenantID).
			Where("knowledge_base_id = ? AND folder_id IN ? AND deleted_at IS NULL", current.KnowledgeBaseID, folderIDs).
			Find(&documents).Error; err != nil {
			return fmt.Errorf("load folder documents: %w", err)
		}
		now := time.Now()
		for _, document := range documents {
			if err := s.softDeleteTx(tx, document, principal.SubjectID, now); err != nil {
				return err
			}
		}
		deletedDocuments = documents
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).
			Where("knowledge_base_id = ? AND folder_id IN ?", current.KnowledgeBaseID, folderIDs).
			Update("folder_id", nil).Error; err != nil {
			return fmt.Errorf("detach folder documents: %w", err)
		}
		for _, node := range subtree {
			if err := tenantdb.MustScoped(tx, principal.TenantID).
				Where("id = ? AND knowledge_base_id = ?", node.ID, current.KnowledgeBaseID).
				Delete(&models.Folder{}).Error; err != nil {
				return fmt.Errorf("delete folder subtree: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, document := range deletedDocuments {
		if err := s.invalidateDeletedDocument(ctx, document); err != nil {
			return err
		}
	}
	return s.recordWorkspaceAudit(ctx, principal, "delete", "folder", folder.ID, folder.Name+" (recursive)")
}

func (s *Service) loadFolder(ctx context.Context, tenantID, knowledgeBaseID, folderID string) (models.Folder, error) {
	return s.loadFolderOn(ctx, s.db, tenantID, knowledgeBaseID, folderID)
}

func (s *Service) loadFolderOn(ctx context.Context, db *gorm.DB, tenantID, knowledgeBaseID, folderID string) (models.Folder, error) {
	var folder models.Folder
	if err := tenantdb.MustScoped(db.WithContext(ctx), tenantID).
		Where("knowledge_base_id = ? AND id = ?", knowledgeBaseID, folderID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Folder{}, apperror.ErrNotFound
		}
		return models.Folder{}, fmt.Errorf("load folder: %w", err)
	}
	return folder, nil
}

func (s *Service) loadFolderByID(ctx context.Context, tenantID, folderID string) (models.Folder, error) {
	return s.loadFolderByIDOn(ctx, s.db, tenantID, folderID)
}

func (s *Service) loadFolderByIDOn(ctx context.Context, db *gorm.DB, tenantID, folderID string) (models.Folder, error) {
	var folder models.Folder
	if err := tenantdb.MustScoped(db.WithContext(ctx), tenantID).Where("id = ?", folderID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Folder{}, apperror.ErrNotFound
		}
		return models.Folder{}, fmt.Errorf("load folder: %w", err)
	}
	return folder, nil
}

func (s *Service) ensureSiblingNameFree(ctx context.Context, tenantID, knowledgeBaseID, parentID, name, excludeID string) error {
	return s.ensureSiblingNameFreeOn(ctx, s.db, tenantID, knowledgeBaseID, parentID, name, excludeID)
}

func (s *Service) ensureSiblingNameFreeOn(ctx context.Context, db *gorm.DB, tenantID, knowledgeBaseID, parentID, name, excludeID string) error {
	var count int64
	query := tenantdb.MustScoped(db.WithContext(ctx), tenantID).Model(&models.Folder{}).
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
