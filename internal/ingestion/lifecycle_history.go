package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// lifecycle_history.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// ListVersions returns the version history of a readable, not-deleted document.
func (s *Service) ListVersions(ctx context.Context, principal authz.Principal, documentID string) (DocumentVersionPage, error) {
	document, err := s.loadActiveDocument(ctx, principal, documentID, "read")
	if err != nil {
		return DocumentVersionPage{}, err
	}
	var versions []models.DocumentVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("document_id = ?", document.ID).Order("version DESC").Find(&versions).Error; err != nil {
		return DocumentVersionPage{}, fmt.Errorf("list document versions: %w", err)
	}
	packages, err := s.readyPackages(ctx, principal.TenantID, versions)
	if err != nil {
		return DocumentVersionPage{}, err
	}
	page := DocumentVersionPage{SchemaVersion: SchemaDocumentVersionPage, DocumentID: document.ID, CurrentID: document.LatestVersionID, Items: make([]DocumentVersionView, 0, len(versions))}
	for _, version := range versions {
		ready := packages[version.ID] || version.Status == StatusEvidenceReady
		page.Items = append(page.Items, DocumentVersionView{
			ID: version.ID, Version: version.Version, ContentHash: version.ContentHash, SizeBytes: version.SizeBytes,
			Status: version.Status, ErrorCode: version.ErrorCode, ErrorMessage: version.ErrorMessage,
			Current: version.ID == document.LatestVersionID, ContentReady: ready,
			Restorable: ready && version.SourceObjectKey != "", CreatedAt: version.CreatedAt,
		})
	}
	return page, nil
}

// Reparse creates a new version from the current version's original bytes. It is
// not an in-place re-run: processing history, chunks and the evidence package
// stay tied to their immutable version.
//
// Retried requests are idempotent. Reparse always reads the same bytes, so the
// target version is derived from the document and the content hash instead of a
// random id. The pipeline key is "document + target version + stage"; a retry
// finds that run and returns it instead of superseding the version again.

// Reparse creates a new version from the current version's original bytes. It is
// not an in-place re-run: processing history, chunks and the evidence package
// stay tied to their immutable version.
//
// Retried requests are idempotent. Reparse always reads the same bytes, so the
// target version is derived from the document and the content hash instead of a
// random id. The pipeline key is "document + target version + stage"; a retry
// finds that run and returns it instead of superseding the version again.
func (s *Service) Reparse(ctx context.Context, principal authz.Principal, documentID string) (UploadResult, error) {
	document, err := s.loadActiveDocument(ctx, principal, documentID, "write")
	if err != nil {
		return UploadResult{}, err
	}
	if document.LatestVersionID == "" {
		return UploadResult{}, apperror.ErrNotReady
	}
	var current models.DocumentVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).First(&current, "id = ?", document.LatestVersionID).Error; err != nil {
		return UploadResult{}, fmt.Errorf("load current version: %w", err)
	}
	if current.SourceObjectKey == "" {
		return UploadResult{}, apperror.ErrNotReady
	}
	content, err := s.objects.Get(ctx, current.SourceObjectKey)
	if err != nil {
		return UploadResult{}, fmt.Errorf("load source object: %w", err)
	}
	if len(content) == 0 {
		return UploadResult{}, apperror.ErrNotReady
	}
	hash := contentHash(content)
	versionID := stableID("ver", document.ID, hash)
	idempotencyKey := "reparse:" + document.ID + ":" + versionID + ":" + StageParse
	if existing, found, err := findPipelineByIdempotencyKey(s.db.WithContext(ctx), principal.TenantID, idempotencyKey); err != nil {
		return UploadResult{}, err
	} else if found {
		return s.loadResult(ctx, principal.TenantID, existing.DocumentID, existing.DocumentVersionID, existing.ID)
	}
	if err := s.checkQuota(ctx, s.db, principal.TenantID, int64(len(content))); err != nil {
		return UploadResult{}, err
	}
	knowledgeBase, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "write")
	if err != nil {
		return UploadResult{}, err
	}
	pipelineID, err := newID("pipe")
	if err != nil {
		return UploadResult{}, err
	}
	objectKey := objectKeyFor(principal.TenantID, document.KnowledgeBaseID, document.ID, versionID, document.Filename)
	if err := s.objects.Put(ctx, objectKey, content, document.MimeType); err != nil {
		return UploadResult{}, err
	}
	cleanupObject := true
	defer func() {
		if cleanupObject {
			_ = s.objects.Delete(context.Background(), objectKey)
		}
	}()
	stageKey := pipelineID + ":" + StageParse
	pipeline := models.PipelineRun{ID: pipelineID, PipelineType: PipelineTypeEvidence, TenantID: principal.TenantID, KnowledgeBaseID: document.KnowledgeBaseID, DocumentID: document.ID, DocumentVersionID: versionID, RequestID: "reparse:" + versionID, TraceID: "reparse:" + versionID, IdempotencyKey: idempotencyKey, Status: StatusStored}
	stage := models.StageRun{PipelineRunID: pipelineID, Stage: StageParse, Status: StagePending, IdempotencyKey: stageKey}
	version := models.DocumentVersion{ID: versionID, DocumentID: document.ID, TenantID: principal.TenantID, KnowledgeBaseID: document.KnowledgeBaseID, ContentHash: hash, SizeBytes: int64(len(content)), SourceObjectKey: objectKey, Status: StatusStored, ACLVersion: knowledgeBase.ACLVersion}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := lockTenantQuota(tx, principal.TenantID); err != nil {
			return err
		}
		// A concurrent retry may have committed while this request waited for
		// the lock. Reusing its run keeps a single target version and pipeline.
		if _, found, err := findPipelineByIdempotencyKey(tx, principal.TenantID, idempotencyKey); err != nil {
			return err
		} else if found {
			return errReparseReused
		}
		if err := s.checkQuota(ctx, tx, principal.TenantID, int64(len(content))); err != nil {
			return err
		}
		if err := supersedeVersion(tx, principal.TenantID, current.ID, "superseded by reparse"); err != nil {
			return err
		}
		if err := cancelOtherPipelines(tx, principal.TenantID, document.ID, versionID); err != nil {
			return err
		}
		var count int64
		if err := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.DocumentVersion{}).Where("document_id = ?", document.ID).Count(&count).Error; err != nil {
			return err
		}
		version.Version = int(count) + 1
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Document{}).Where("tenant_id = ? AND id = ?", principal.TenantID, document.ID).Updates(map[string]any{"status": StatusStored, "latest_version_id": versionID, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := tx.Create(&pipeline).Error; err != nil {
			return err
		}
		if err := tx.Create(&stage).Error; err != nil {
			return err
		}
		task := s.taskFor(principal, document, version, pipeline, stage, StageParse)
		payload, marshalErr := json.Marshal(task)
		if marshalErr != nil {
			return marshalErr
		}
		return tx.Create(&models.OutboxEvent{TenantID: principal.TenantID, AggregateType: "document_version", AggregateID: versionID, EventType: "document.parse", IdempotencyKey: stageKey, Payload: string(payload)}).Error
	})
	if errors.Is(err, errReparseReused) {
		// The object key is deterministic per target version, so the committed
		// run owns the bytes we just wrote; deleting them would break it.
		cleanupObject = false
		existing, found, lookupErr := findPipelineByIdempotencyKey(s.db.WithContext(ctx), principal.TenantID, idempotencyKey)
		if lookupErr != nil {
			return UploadResult{}, lookupErr
		}
		if found {
			return s.loadResult(ctx, principal.TenantID, existing.DocumentID, existing.DocumentVersionID, existing.ID)
		}
		return UploadResult{}, fmt.Errorf("reparse document: reused run disappeared")
	}
	if err != nil {
		if existing, found, lookupErr := findPipelineByIdempotencyKey(s.db.WithContext(ctx), principal.TenantID, idempotencyKey); lookupErr == nil && found {
			cleanupObject = false
			return s.loadResult(ctx, principal.TenantID, existing.DocumentID, existing.DocumentVersionID, existing.ID)
		}
		return UploadResult{}, fmt.Errorf("reparse document: %w", err)
	}
	cleanupObject = false
	return s.loadResult(ctx, principal.TenantID, document.ID, versionID, pipelineID)
}

// findPipelineByIdempotencyKey loads the run a prior request created under the
// same document + target version + stage key, so a retry can reuse it.

// findPipelineByIdempotencyKey loads the run a prior request created under the
// same document + target version + stage key, so a retry can reuse it.
func findPipelineByIdempotencyKey(db *gorm.DB, tenantID, key string) (models.PipelineRun, bool, error) {
	var pipeline models.PipelineRun
	err := db.Where("tenant_id = ? AND idempotency_key = ?", tenantID, key).First(&pipeline).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.PipelineRun{}, false, nil
	}
	if err != nil {
		return models.PipelineRun{}, false, fmt.Errorf("load reparse pipeline: %w", err)
	}
	return pipeline, true, nil
}

// ActivateVersion rolls the document back to an already processed version. A
// superseded version keeps its chunks and package; only their status changed, so
// activation flips them back instead of re-embedding content. A new version
// would be a reparse, not a rollback.

// ActivateVersion rolls the document back to an already processed version. A
// superseded version keeps its chunks and package; only their status changed, so
// activation flips them back instead of re-embedding content. A new version
// would be a reparse, not a rollback.
func (s *Service) ActivateVersion(ctx context.Context, principal authz.Principal, documentID, versionID string) (DocumentView, error) {
	if err := requireSessionPrincipal(principal); err != nil {
		return DocumentView{}, err
	}
	document, err := s.loadActiveDocument(ctx, principal, documentID, "write")
	if err != nil {
		return DocumentView{}, err
	}
	var target models.DocumentVersion
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("document_id = ? AND id = ?", document.ID, versionID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DocumentView{}, apperror.ErrNotFound
		}
		return DocumentView{}, fmt.Errorf("load target version: %w", err)
	}
	ready, err := s.versionReady(ctx, principal.TenantID, target)
	if err != nil {
		return DocumentView{}, err
	}
	if !ready || target.SourceObjectKey == "" {
		return DocumentView{}, apperror.ErrNotReady
	}
	if document.LatestVersionID == target.ID {
		return s.Get(ctx, principal, document.ID)
	}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if document.LatestVersionID != "" {
			if err := supersedeVersion(tx, principal.TenantID, document.LatestVersionID, "superseded by rollback"); err != nil {
				return err
			}
		}
		if err := tx.Model(&models.DocumentVersion{}).Where("tenant_id = ? AND id = ?", principal.TenantID, target.ID).Updates(map[string]any{"status": StatusEvidenceReady, "error_code": "", "error_message": "", "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Chunk{}).Where("tenant_id = ? AND document_version_id = ? AND parent_id IS NULL", principal.TenantID, target.ID).Update("status", StatusParent).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Chunk{}).Where("tenant_id = ? AND document_version_id = ? AND parent_id IS NOT NULL", principal.TenantID, target.ID).Update("status", StatusEvidenceReady).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.EvidencePackage{}).Where("tenant_id = ? AND document_version_id = ?", principal.TenantID, target.ID).Updates(map[string]any{"status": StatusEvidenceReady, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := cancelOtherPipelines(tx, principal.TenantID, document.ID, target.ID); err != nil {
			return err
		}
		return tx.Model(&models.Document{}).Where("tenant_id = ? AND id = ?", principal.TenantID, document.ID).Updates(map[string]any{"status": StatusEvidenceReady, "latest_version_id": target.ID, "updated_at": time.Now()}).Error
	})
	if err != nil {
		return DocumentView{}, fmt.Errorf("activate document version: %w", err)
	}
	if err := s.recordDocumentAudit(ctx, principal, "rollback", document.ID); err != nil {
		return DocumentView{}, err
	}
	return s.Get(ctx, principal, document.ID)
}

// Delete is a soft delete with a retention window. Versions, chunks and the
// evidence package are left untouched so Restore can bring the exact published
// content back; retrieval excludes the document through documents.deleted_at.
// In-flight work is cancelled so a late worker cannot publish a deleted version.
