package ingestion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// lifecycle_deletion.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// Delete is a soft delete with a retention window. Versions, chunks and the
// evidence package are left untouched so Restore can bring the exact published
// content back; retrieval excludes the document through documents.deleted_at.
// In-flight work is cancelled so a late worker cannot publish a deleted version.
func (s *Service) Delete(ctx context.Context, principal authz.Principal, documentID string) error {
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "delete"); err != nil {
		return err
	}
	return s.softDelete(ctx, document, principal.SubjectID)
}

// TombstoneExternal is the connector deletion path. Authorization happened when
// the connector was configured; the worker rechecks tenant and knowledge-base
// binding before calling this, and the document lifecycle itself is identical to
// a user delete so a late ingestion task cannot publish deleted content.

// TombstoneExternal is the connector deletion path. Authorization happened when
// the connector was configured; the worker rechecks tenant and knowledge-base
// binding before calling this, and the document lifecycle itself is identical to
// a user delete so a late ingestion task cannot publish deleted content.
func (s *Service) TombstoneExternal(ctx context.Context, tenantID, documentID, actorID string) error {
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("id = ?", documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load connector document: %w", err)
	}
	return s.softDelete(ctx, document, actorID)
}

func (s *Service) softDelete(ctx context.Context, document models.Document, actorID string) error {
	if document.DeletedAt != nil {
		return nil
	}
	now := time.Now()
	purgeAfter := now.Add(s.retention)
	err := tenantdb.InTx(ctx, s.db, document.TenantID, func(tx *gorm.DB) error {
		if err := tenantdb.MustScoped(tx, document.TenantID).Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{
			"status": StatusCancelled, "deleted_at": &now, "deleted_by": actorID,
			"purge_after": &purgeAfter, "restored_at": nil, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		var versions []models.DocumentVersion
		if err := tenantdb.MustScoped(tx, document.TenantID).Where("document_id = ?", document.ID).Find(&versions).Error; err != nil {
			return err
		}
		versionIDs := make([]string, 0, len(versions))
		for _, version := range versions {
			versionIDs = append(versionIDs, version.ID)
		}
		if len(versionIDs) > 0 {
			if err := cancelPendingOutbox(tx, document.TenantID, versionIDs); err != nil {
				return err
			}
		}
		return cancelDocumentPipelines(tx, document.TenantID, document.ID)
	})
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Restore undoes a soft delete while the retention window is still open. A
// version that never reached EVIDENCE_READY comes back as STORED and must be
// reparsed; a ready version comes back searchable immediately.

// Restore undoes a soft delete while the retention window is still open. A
// version that never reached EVIDENCE_READY comes back as STORED and must be
// reparsed; a ready version comes back searchable immediately.
func (s *Service) Restore(ctx context.Context, principal authz.Principal, documentID string) (models.Document, error) {
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Document{}, apperror.ErrNotFound
		}
		return models.Document{}, fmt.Errorf("load document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "write"); err != nil {
		return models.Document{}, err
	}
	if document.DeletedAt == nil {
		return document, nil
	}
	if document.PurgeAfter != nil && document.PurgeAfter.Before(time.Now()) {
		return models.Document{}, fmt.Errorf("the retention window for this document has expired")
	}
	status := StatusStored
	if document.LatestVersionID != "" {
		var version models.DocumentVersion
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&version, "id = ?", document.LatestVersionID).Error; err == nil {
			if ready, readyErr := s.versionReady(ctx, principal.TenantID, version); readyErr != nil {
				return models.Document{}, readyErr
			} else if ready {
				status = StatusEvidenceReady
			}
		}
	}
	now := time.Now()
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{
		"status": status, "deleted_at": nil, "deleted_by": "", "purge_after": nil, "restored_at": &now, "updated_at": now,
	}).Error; err != nil {
		return models.Document{}, fmt.Errorf("restore document: %w", err)
	}
	// Read into a fresh value: reusing the loaded struct keeps the pre-restore
	// deleted_at/purge_after in the response.
	var restored models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&restored, "id = ?", document.ID).Error; err != nil {
		return models.Document{}, err
	}
	return restored, nil
}

// Purge removes one deleted document and every derived row. Only documents that
// are already soft-deleted can be purged, so a stray id cannot destroy live
// content.

// Purge removes one deleted document and every derived row. Only documents that
// are already soft-deleted can be purged, so a stray id cannot destroy live
// content.
func (s *Service) Purge(ctx context.Context, principal authz.Principal, documentID string) error {
	if err := requireSessionPrincipal(principal); err != nil {
		return err
	}
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "delete"); err != nil {
		return err
	}
	if document.DeletedAt == nil {
		return apperror.Invalidf("document must be deleted before it can be purged")
	}
	if err := s.purgeDocument(ctx, document.TenantID, document.ID); err != nil {
		return err
	}
	return s.recordDocumentAudit(ctx, principal, "purge", document.ID)
}

// requireSessionPrincipal keeps hard deletes and active-version changes
// attributable to a person. API keys can ingest and read, but those two actions
// change or destroy published content.

// requireSessionPrincipal keeps hard deletes and active-version changes
// attributable to a person. API keys can ingest and read, but those two actions
// change or destroy published content.
func requireSessionPrincipal(principal authz.Principal) error {
	if principal.CredentialType == "api_key" {
		return apperror.ErrForbidden
	}
	return nil
}

func (s *Service) recordDocumentAudit(ctx context.Context, principal authz.Principal, action, documentID string) error {
	if err := s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: action,
		ResourceType: "document", ResourceID: documentID, Allowed: true,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error; err != nil {
		return fmt.Errorf("record %s audit: %w", action, err)
	}
	return nil
}

// PurgeExpired is the retention job. It returns the number of documents purged
// and never touches a document whose purge_after is still in the future.

// PurgeExpired is the retention job. It returns the number of documents purged
// and never touches a document whose purge_after is still in the future.
func (s *Service) PurgeExpired(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	var documents []models.Document
	if err := tenantdb.Unscoped(s.db.WithContext(ctx), "retention: purge expired documents across tenants").
		Where("deleted_at IS NOT NULL AND purge_after IS NOT NULL AND purge_after <= ?", time.Now()).
		Order("purge_after").Limit(limit).Find(&documents).Error; err != nil {
		return 0, fmt.Errorf("list expired documents: %w", err)
	}
	purged := 0
	for _, document := range documents {
		if err := s.purgeDocument(ctx, document.TenantID, document.ID); err != nil {
			return purged, err
		}
		purged++
	}
	return purged, nil
}

func (s *Service) purgeDocument(ctx context.Context, tenantID, documentID string) error {
	var versions []models.DocumentVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("document_id = ?", documentID).Find(&versions).Error; err != nil {
		return fmt.Errorf("load versions for purge: %w", err)
	}
	versionIDs := make([]string, 0, len(versions))
	for _, version := range versions {
		versionIDs = append(versionIDs, version.ID)
		if version.SourceObjectKey != "" {
			if err := s.objects.Delete(ctx, version.SourceObjectKey); err != nil {
				return err
			}
		}
		if version.NormalizedKey != "" {
			if err := s.objects.Delete(ctx, version.NormalizedKey); err != nil {
				return err
			}
		}
	}
	err := tenantdb.InTx(ctx, s.db, tenantID, func(tx *gorm.DB) error {
		if len(versionIDs) > 0 {
			if err := tenantdb.MustScoped(tx, tenantID).Where("document_version_id IN ?", versionIDs).Delete(&models.Chunk{}).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, tenantID).Where("document_version_id IN ?", versionIDs).Delete(&models.EvidencePackage{}).Error; err != nil {
				return err
			}
			if err := tenantdb.MustScoped(tx, tenantID).Where("document_version_id IN ?", versionIDs).Delete(&models.TaskDeadLetter{}).Error; err != nil {
				return err
			}
		}
		var pipelines []models.PipelineRun
		if err := tenantdb.MustScoped(tx, tenantID).Where("document_id = ?", documentID).Find(&pipelines).Error; err != nil {
			return err
		}
		pipelineIDs := make([]string, 0, len(pipelines))
		for _, pipeline := range pipelines {
			pipelineIDs = append(pipelineIDs, pipeline.ID)
		}
		if len(pipelineIDs) > 0 {
			if err := tx.Where("pipeline_run_id IN ?", pipelineIDs).Delete(&models.StageRun{}).Error; err != nil {
				return err
			}
			// Later stages are published with the pipeline run as their
			// aggregate, so both aggregate kinds must be purged.
			if err := tenantdb.MustScoped(tx, tenantID).Where("aggregate_id IN ?", append(append([]string{}, versionIDs...), pipelineIDs...)).Delete(&models.OutboxEvent{}).Error; err != nil {
				return err
			}
			for _, pipelineID := range pipelineIDs {
				if err := tx.Where("idempotency_key LIKE ?", pipelineID+":%").Delete(&models.TaskInbox{}).Error; err != nil {
					return err
				}
			}
		} else if len(versionIDs) > 0 {
			if err := tenantdb.MustScoped(tx, tenantID).Where("aggregate_id IN ?", versionIDs).Delete(&models.OutboxEvent{}).Error; err != nil {
				return err
			}
		}
		if err := tenantdb.MustScoped(tx, tenantID).Where("document_id = ?", documentID).Delete(&models.PipelineRun{}).Error; err != nil {
			return err
		}
		if err := tenantdb.MustScoped(tx, tenantID).Where("document_id = ?", documentID).Delete(&models.DocumentVersion{}).Error; err != nil {
			return err
		}
		return tenantdb.MustScoped(tx, tenantID).Where("id = ?", documentID).Delete(&models.Document{}).Error
	})
	if err != nil {
		return fmt.Errorf("purge document: %w", err)
	}
	// PostgreSQL needs no external cleanup, but an external index adapter must
	// remove the version. A failure here is logged rather than resurrecting a
	// document whose database rows are already gone.
	if s.index != nil {
		for _, versionID := range versionIDs {
			if err := s.index.DeleteVersion(ctx, versionID); err != nil {
				slog.Warn("index_delete_failed", "document_id", documentID, "document_version_id", versionID, "error", err)
			}
		}
	}
	return nil
}

func (s *Service) loadActiveDocument(ctx context.Context, principal authz.Principal, documentID, action string) (models.Document, error) {
	if documentID == "" {
		return models.Document{}, apperror.ErrNotFound
	}
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Document{}, apperror.ErrNotFound
		}
		return models.Document{}, fmt.Errorf("load document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, action); err != nil {
		return models.Document{}, err
	}
	if document.DeletedAt != nil {
		return models.Document{}, apperror.ErrNotFound
	}
	return document, nil
}

func supersedeVersion(tx *gorm.DB, tenantID, versionID, reason string) error {
	if err := tenantdb.MustScoped(tx, tenantID).Model(&models.DocumentVersion{}).Where("id = ?", versionID).Updates(map[string]any{
		"status": StatusCancelled, "error_code": "superseded", "error_message": reason, "updated_at": time.Now(),
	}).Error; err != nil {
		return err
	}
	if err := tenantdb.MustScoped(tx, tenantID).Model(&models.Chunk{}).Where("document_version_id = ?", versionID).Update("status", StatusStale).Error; err != nil {
		return err
	}
	if err := tenantdb.MustScoped(tx, tenantID).Model(&models.EvidencePackage{}).Where("document_version_id = ?", versionID).Update("status", StatusStale).Error; err != nil {
		return err
	}
	return cancelPendingOutbox(tx, tenantID, []string{versionID})
}

func cancelDocumentPipelines(tx *gorm.DB, tenantID, documentID string) error {
	now := time.Now()
	var pipelines []models.PipelineRun
	if err := tenantdb.MustScoped(tx, tenantID).Where("document_id = ? AND status NOT IN ?", documentID, []string{StatusEvidenceReady, StatusCancelled, StatusFailed}).Find(&pipelines).Error; err != nil {
		return err
	}
	if len(pipelines) == 0 {
		return nil
	}
	pipelineIDs := make([]string, 0, len(pipelines))
	for _, pipeline := range pipelines {
		pipelineIDs = append(pipelineIDs, pipeline.ID)
	}
	if err := tx.Model(&models.StageRun{}).Where("pipeline_run_id IN ? AND status IN ?", pipelineIDs, []string{StagePending, StageProcessing}).Updates(map[string]any{"status": StageCancelled, "error_code": "deleted", "error_message": "document deleted", "updated_at": now}).Error; err != nil {
		return err
	}
	return tenantdb.MustScoped(tx, tenantID).Model(&models.PipelineRun{}).Where("id IN ?", pipelineIDs).Updates(map[string]any{"status": StatusCancelled, "error_code": "deleted", "error_message": "document deleted", "updated_at": now}).Error
}
