package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BatchDelete  = "delete"
	BatchReparse = "reparse"
	BatchMove    = "move"
	BatchTag     = "tag"
	BatchUntag   = "untag"
)

// BatchInput is one workspace batch request. FolderID uses "" for the root
// level and is required for move; TagIDs is required for tag/untag.
type BatchInput struct {
	Action      string
	DocumentIDs []string
	FolderID    string
	TagIDs      []string
}

// BatchResult reports one document. Partial success is expected: each document
// carries its own outcome instead of failing the whole request.
type BatchResult struct {
	DocumentID   string
	OK           bool
	ErrorCode    string
	ErrorMessage string
}

func (s *Service) BatchDocuments(ctx context.Context, principal authz.Principal, knowledgeBaseID string, input BatchInput) ([]BatchResult, error) {
	action := input.Action
	switch action {
	case BatchDelete, BatchReparse, BatchMove, BatchTag, BatchUntag:
	default:
		return nil, apperror.Invalidf("unsupported batch action")
	}
	documents := uniqueStrings(input.DocumentIDs)
	if len(documents) == 0 {
		return nil, apperror.Invalidf("document ids are required")
	}
	if len(documents) > maxBatchDocuments {
		return nil, apperror.Invalidf("at most %d documents per batch", maxBatchDocuments)
	}
	actionName := "write"
	if action == BatchDelete {
		actionName = "delete"
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, actionName); err != nil {
		return nil, err
	}
	if action == BatchMove && input.FolderID != "" {
		if _, err := s.loadFolder(ctx, principal.TenantID, knowledgeBaseID, input.FolderID); err != nil {
			return nil, err
		}
	}
	var tagIDs []string
	if action == BatchTag || action == BatchUntag {
		tagIDs = uniqueStrings(input.TagIDs)
		if len(tagIDs) == 0 {
			return nil, apperror.Invalidf("tag ids are required")
		}
		if err := s.validateTagIDs(ctx, principal.TenantID, knowledgeBaseID, tagIDs); err != nil {
			return nil, err
		}
	}
	results := make([]BatchResult, 0, len(documents))
	for _, documentID := range documents {
		result := BatchResult{DocumentID: documentID}
		active, err := s.documentInKnowledgeBase(ctx, principal.TenantID, knowledgeBaseID, documentID)
		if err != nil {
			return nil, err
		}
		if !active {
			result.ErrorCode = "not_found"
			results = append(results, result)
			continue
		}
		switch action {
		case BatchDelete:
			err = s.Delete(ctx, principal, documentID)
		case BatchReparse:
			_, err = s.Reparse(ctx, principal, documentID)
		case BatchMove:
			err = s.moveDocument(ctx, principal, knowledgeBaseID, documentID, input.FolderID)
		case BatchTag:
			err = s.tagDocument(ctx, principal, knowledgeBaseID, documentID, tagIDs)
		case BatchUntag:
			err = s.untagDocument(ctx, principal, knowledgeBaseID, documentID, tagIDs)
		}
		if err != nil {
			result.ErrorCode, result.ErrorMessage = batchFailure(err)
		} else {
			result.OK = true
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) documentInKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID, documentID string) (bool, error) {
	var count int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Document{}).
		Where("id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", documentID, knowledgeBaseID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check batch document: %w", err)
	}
	return count > 0, nil
}

func (s *Service) moveDocument(ctx context.Context, principal authz.Principal, knowledgeBaseID, documentID, folderID string) error {
	err := tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := persistence.LockKnowledgeBaseXact(tx, principal.TenantID, knowledgeBaseID); err != nil {
			return err
		}
		// The folder can be deleted between the batch pre-check and this update;
		// re-read it under the lock.
		if folderID != "" {
			if _, err := s.loadFolderOn(ctx, tx, principal.TenantID, knowledgeBaseID, folderID); err != nil {
				return err
			}
		}
		updates := map[string]any{"folder_id": nil, "updated_at": time.Now().UTC()}
		if folderID != "" {
			updates["folder_id"] = folderID
		}
		return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.Document{}).
			Where("id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", documentID, knowledgeBaseID).
			Updates(updates).Error
	})
	if err != nil {
		return fmt.Errorf("move document: %w", err)
	}
	return s.recordDocumentBatchAudit(ctx, principal, documentID, map[string]any{"action": BatchMove, "folderId": folderID})
}

func (s *Service) tagDocument(ctx context.Context, principal authz.Principal, knowledgeBaseID, documentID string, tagIDs []string) error {
	rows := make([]models.DocumentTag, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		rows = append(rows, models.DocumentTag{
			TenantID: principal.TenantID, KnowledgeBaseID: knowledgeBaseID,
			DocumentID: documentID, TagID: tagID,
		})
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
		return fmt.Errorf("tag document: %w", err)
	}
	return s.recordDocumentBatchAudit(ctx, principal, documentID, map[string]any{"action": BatchTag, "tagIds": tagIDs})
}

func (s *Service) untagDocument(ctx context.Context, principal authz.Principal, knowledgeBaseID, documentID string, tagIDs []string) error {
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("document_id = ? AND knowledge_base_id = ? AND tag_id IN ?", documentID, knowledgeBaseID, tagIDs).
		Delete(&models.DocumentTag{}).Error; err != nil {
		return fmt.Errorf("untag document: %w", err)
	}
	return s.recordDocumentBatchAudit(ctx, principal, documentID, map[string]any{"action": BatchUntag, "tagIds": tagIDs})
}

func (s *Service) recordDocumentBatchAudit(ctx context.Context, principal authz.Principal, documentID string, detail map[string]any) error {
	reason, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode batch audit: %w", err)
	}
	return s.recordWorkspaceAudit(ctx, principal, "write", "document", documentID, string(reason))
}

func batchFailure(err error) (string, string) {
	switch {
	case errors.Is(err, apperror.ErrNotFound):
		return "not_found", ""
	case errors.Is(err, apperror.ErrForbidden):
		return "forbidden", ""
	case errors.Is(err, apperror.ErrNotReady):
		return "not_ready", ""
	case errors.Is(err, apperror.ErrConflict):
		return "conflict", ""
	}
	if validation, ok := apperror.AsValidation(err); ok {
		return "invalid_request", validation.Error()
	}
	return "internal_error", ""
}
