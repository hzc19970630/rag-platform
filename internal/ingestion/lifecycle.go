package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const SchemaDocumentVersionPage = "DocumentVersionPageV1"

var errReparseReused = errors.New("reparse target already exists")

// DocumentVersionView is one immutable version in the history. It never
// exposes the source or normalized object keys, because those stay server side
// like the current-version projections in preview.go.
type DocumentVersionView struct {
	ID           string    `json:"id"`
	Version      int       `json:"version"`
	ContentHash  string    `json:"contentHash"`
	SizeBytes    int64     `json:"sizeBytes"`
	Status       string    `json:"status"`
	ErrorCode    string    `json:"errorCode"`
	ErrorMessage string    `json:"errorMessage"`
	Current      bool      `json:"current"`
	ContentReady bool      `json:"contentReady"`
	Restorable   bool      `json:"restorable"`
	CreatedAt    time.Time `json:"createdAt"`
}

type DocumentVersionPage struct {
	SchemaVersion string                `json:"schemaVersion"`
	DocumentID    string                `json:"documentId"`
	CurrentID     string                `json:"currentVersionId"`
	Items         []DocumentVersionView `json:"items"`
}

// ListVersions returns the version history of a readable, not-deleted document.
func (s *Service) ListVersions(ctx context.Context, principal authz.Principal, documentID string) (DocumentVersionPage, error) {
	document, err := s.loadActiveDocument(ctx, principal, documentID, "read")
	if err != nil {
		return DocumentVersionPage{}, err
	}
	var versions []models.DocumentVersion
	if err := s.db.WithContext(ctx).Where("document_id = ?", document.ID).Order("version DESC").Find(&versions).Error; err != nil {
		return DocumentVersionPage{}, fmt.Errorf("list document versions: %w", err)
	}
	packages, err := s.readyPackages(ctx, versions)
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
func (s *Service) Reparse(ctx context.Context, principal authz.Principal, documentID string) (UploadResult, error) {
	document, err := s.loadActiveDocument(ctx, principal, documentID, "write")
	if err != nil {
		return UploadResult{}, err
	}
	if document.LatestVersionID == "" {
		return UploadResult{}, apperror.ErrNotReady
	}
	var current models.DocumentVersion
	if err := s.db.WithContext(ctx).First(&current, "id = ?", document.LatestVersionID).Error; err != nil {
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
		return s.loadResult(ctx, existing.DocumentID, existing.DocumentVersionID, existing.ID)
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
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if err := supersedeVersion(tx, current.ID, "superseded by reparse"); err != nil {
			return err
		}
		if err := cancelOtherPipelines(tx, document.ID, versionID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.DocumentVersion{}).Where("document_id = ?", document.ID).Count(&count).Error; err != nil {
			return err
		}
		version.Version = int(count) + 1
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{"status": StatusStored, "latest_version_id": versionID, "updated_at": time.Now()}).Error; err != nil {
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
			return s.loadResult(ctx, existing.DocumentID, existing.DocumentVersionID, existing.ID)
		}
		return UploadResult{}, fmt.Errorf("reparse document: reused run disappeared")
	}
	if err != nil {
		if existing, found, lookupErr := findPipelineByIdempotencyKey(s.db.WithContext(ctx), principal.TenantID, idempotencyKey); lookupErr == nil && found {
			cleanupObject = false
			return s.loadResult(ctx, existing.DocumentID, existing.DocumentVersionID, existing.ID)
		}
		return UploadResult{}, fmt.Errorf("reparse document: %w", err)
	}
	cleanupObject = false
	return s.loadResult(ctx, document.ID, versionID, pipelineID)
}

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
func (s *Service) ActivateVersion(ctx context.Context, principal authz.Principal, documentID, versionID string) (DocumentView, error) {
	if err := requireSessionPrincipal(principal); err != nil {
		return DocumentView{}, err
	}
	document, err := s.loadActiveDocument(ctx, principal, documentID, "write")
	if err != nil {
		return DocumentView{}, err
	}
	var target models.DocumentVersion
	if err := s.db.WithContext(ctx).Where("document_id = ? AND id = ?", document.ID, versionID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DocumentView{}, apperror.ErrNotFound
		}
		return DocumentView{}, fmt.Errorf("load target version: %w", err)
	}
	ready, err := s.versionReady(ctx, target)
	if err != nil {
		return DocumentView{}, err
	}
	if !ready || target.SourceObjectKey == "" {
		return DocumentView{}, apperror.ErrNotReady
	}
	if document.LatestVersionID == target.ID {
		return s.Get(ctx, principal, document.ID)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if document.LatestVersionID != "" {
			if err := supersedeVersion(tx, document.LatestVersionID, "superseded by rollback"); err != nil {
				return err
			}
		}
		if err := tx.Model(&models.DocumentVersion{}).Where("id = ?", target.ID).Updates(map[string]any{"status": StatusEvidenceReady, "error_code": "", "error_message": "", "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Chunk{}).Where("document_version_id = ? AND parent_id IS NULL", target.ID).Update("status", StatusParent).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Chunk{}).Where("document_version_id = ? AND parent_id IS NOT NULL", target.ID).Update("status", StatusEvidenceReady).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.EvidencePackage{}).Where("document_version_id = ?", target.ID).Updates(map[string]any{"status": StatusEvidenceReady, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := cancelOtherPipelines(tx, document.ID, target.ID); err != nil {
			return err
		}
		return tx.Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{"status": StatusEvidenceReady, "latest_version_id": target.ID, "updated_at": time.Now()}).Error
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
func (s *Service) Delete(ctx context.Context, principal authz.Principal, documentID string) error {
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, documentID).First(&document).Error; err != nil {
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
func (s *Service) TombstoneExternal(ctx context.Context, tenantID, documentID, actorID string) error {
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, documentID).First(&document).Error; err != nil {
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{
			"status": StatusCancelled, "deleted_at": &now, "deleted_by": actorID,
			"purge_after": &purgeAfter, "restored_at": nil, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		var versions []models.DocumentVersion
		if err := tx.Where("document_id = ?", document.ID).Find(&versions).Error; err != nil {
			return err
		}
		versionIDs := make([]string, 0, len(versions))
		for _, version := range versions {
			versionIDs = append(versionIDs, version.ID)
		}
		if len(versionIDs) > 0 {
			if err := cancelPendingOutbox(tx, versionIDs); err != nil {
				return err
			}
		}
		return cancelDocumentPipelines(tx, document.ID)
	})
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Restore undoes a soft delete while the retention window is still open. A
// version that never reached EVIDENCE_READY comes back as STORED and must be
// reparsed; a ready version comes back searchable immediately.
func (s *Service) Restore(ctx context.Context, principal authz.Principal, documentID string) (models.Document, error) {
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, documentID).First(&document).Error; err != nil {
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
		if err := s.db.WithContext(ctx).First(&version, "id = ?", document.LatestVersionID).Error; err == nil {
			if ready, readyErr := s.versionReady(ctx, version); readyErr != nil {
				return models.Document{}, readyErr
			} else if ready {
				status = StatusEvidenceReady
			}
		}
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{
		"status": status, "deleted_at": nil, "deleted_by": "", "purge_after": nil, "restored_at": &now, "updated_at": now,
	}).Error; err != nil {
		return models.Document{}, fmt.Errorf("restore document: %w", err)
	}
	// Read into a fresh value: reusing the loaded struct keeps the pre-restore
	// deleted_at/purge_after in the response.
	var restored models.Document
	if err := s.db.WithContext(ctx).First(&restored, "id = ?", document.ID).Error; err != nil {
		return models.Document{}, err
	}
	return restored, nil
}

// Purge removes one deleted document and every derived row. Only documents that
// are already soft-deleted can be purged, so a stray id cannot destroy live
// content.
func (s *Service) Purge(ctx context.Context, principal authz.Principal, documentID string) error {
	if err := requireSessionPrincipal(principal); err != nil {
		return err
	}
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, documentID).First(&document).Error; err != nil {
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
	if err := s.purgeDocument(ctx, document.ID); err != nil {
		return err
	}
	return s.recordDocumentAudit(ctx, principal, "purge", document.ID)
}

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
func (s *Service) PurgeExpired(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	var documents []models.Document
	if err := s.db.WithContext(ctx).Where("deleted_at IS NOT NULL AND purge_after IS NOT NULL AND purge_after <= ?", time.Now()).Order("purge_after").Limit(limit).Find(&documents).Error; err != nil {
		return 0, fmt.Errorf("list expired documents: %w", err)
	}
	purged := 0
	for _, document := range documents {
		if err := s.purgeDocument(ctx, document.ID); err != nil {
			return purged, err
		}
		purged++
	}
	return purged, nil
}

func (s *Service) purgeDocument(ctx context.Context, documentID string) error {
	var versions []models.DocumentVersion
	if err := s.db.WithContext(ctx).Where("document_id = ?", documentID).Find(&versions).Error; err != nil {
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(versionIDs) > 0 {
			if err := tx.Where("document_version_id IN ?", versionIDs).Delete(&models.Chunk{}).Error; err != nil {
				return err
			}
			if err := tx.Where("document_version_id IN ?", versionIDs).Delete(&models.EvidencePackage{}).Error; err != nil {
				return err
			}
			if err := tx.Where("document_version_id IN ?", versionIDs).Delete(&models.TaskDeadLetter{}).Error; err != nil {
				return err
			}
		}
		var pipelines []models.PipelineRun
		if err := tx.Where("document_id = ?", documentID).Find(&pipelines).Error; err != nil {
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
			if err := tx.Where("aggregate_id IN ?", append(append([]string{}, versionIDs...), pipelineIDs...)).Delete(&models.OutboxEvent{}).Error; err != nil {
				return err
			}
			for _, pipelineID := range pipelineIDs {
				if err := tx.Where("idempotency_key LIKE ?", pipelineID+":%").Delete(&models.TaskInbox{}).Error; err != nil {
					return err
				}
			}
		} else if len(versionIDs) > 0 {
			if err := tx.Where("aggregate_id IN ?", versionIDs).Delete(&models.OutboxEvent{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&models.PipelineRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&models.DocumentVersion{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", documentID).Delete(&models.Document{}).Error
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
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, documentID).First(&document).Error; err != nil {
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

func (s *Service) readyPackages(ctx context.Context, versions []models.DocumentVersion) (map[string]bool, error) {
	ready := map[string]bool{}
	ids := make([]string, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	if len(ids) == 0 {
		return ready, nil
	}
	// A superseded version's package is STALE but intact, so it stays
	// restorable: rollback reactivates the existing chunks instead of
	// re-embedding them.
	var packages []models.EvidencePackage
	if err := s.db.WithContext(ctx).Where("document_version_id IN ? AND status IN ?", ids, []string{StatusEvidenceReady, StatusStale}).Find(&packages).Error; err != nil {
		return nil, fmt.Errorf("load evidence packages: %w", err)
	}
	for _, pkg := range packages {
		ready[pkg.DocumentVersionID] = true
	}
	return ready, nil
}

func (s *Service) versionReady(ctx context.Context, version models.DocumentVersion) (bool, error) {
	if version.Status == StatusEvidenceReady {
		return true, nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.EvidencePackage{}).Where("document_version_id = ? AND status IN ?", version.ID, []string{StatusEvidenceReady, StatusStale}).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check evidence package: %w", err)
	}
	return count > 0, nil
}

func supersedeVersion(tx *gorm.DB, versionID, reason string) error {
	if err := tx.Model(&models.DocumentVersion{}).Where("id = ?", versionID).Updates(map[string]any{
		"status": StatusCancelled, "error_code": "superseded", "error_message": reason, "updated_at": time.Now(),
	}).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.Chunk{}).Where("document_version_id = ?", versionID).Update("status", StatusStale).Error; err != nil {
		return err
	}
	if err := tx.Model(&models.EvidencePackage{}).Where("document_version_id = ?", versionID).Update("status", StatusStale).Error; err != nil {
		return err
	}
	return cancelPendingOutbox(tx, []string{versionID})
}

func cancelPendingOutbox(tx *gorm.DB, versionIDs []string) error {
	now := time.Now()
	return tx.Model(&models.OutboxEvent{}).Where("aggregate_id IN ? AND published_at IS NULL", versionIDs).Updates(map[string]any{"published_at": &now, "last_error": "cancelled"}).Error
}

func cancelOtherPipelines(tx *gorm.DB, documentID, keepVersionID string) error {
	now := time.Now()
	return tx.Model(&models.PipelineRun{}).Where("document_id = ? AND document_version_id <> ? AND status NOT IN ?", documentID, keepVersionID, []string{StatusEvidenceReady, StatusCancelled, StatusFailed}).
		Updates(map[string]any{"status": StatusCancelled, "error_code": "superseded", "error_message": "superseded by another version", "updated_at": now}).Error
}

// restartTargetStage is the first stage that never succeeded. A missing stage
// means processing stopped before it was created. An empty result means the
// pipeline already finished every stage.
func restartTargetStage(stages []models.StageRun) string {
	byStage := make(map[string]models.StageRun, len(stages))
	for _, stage := range stages {
		byStage[stage.Stage] = stage
	}
	for _, stage := range stageOrder {
		if current, ok := byStage[stage]; !ok || current.Status != StageSucceeded {
			return stage
		}
	}
	return ""
}

// restartDeletionCancelledPipeline resumes a pipeline that a document deletion
// cancelled while its stages were still running. A reappearing connector item
// reuses the same document, version and stage keys, so the pipeline must continue
// from the first stage that never succeeded instead of being returned as a dead
// record. A version whose evidence survived the delete is already searchable, so
// only its stale cancelled records are settled.
func (s *Service) restartDeletionCancelledPipeline(ctx context.Context, principal authz.Principal, pipelineID string) (bool, error) {
	var pipeline models.PipelineRun
	if err := s.db.WithContext(ctx).First(&pipeline, "id = ?", pipelineID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load pipeline for restart: %w", err)
	}
	if pipeline.Status != StatusCancelled || pipeline.ErrorCode != "deleted" {
		return false, nil
	}
	var document models.Document
	if err := s.db.WithContext(ctx).First(&document, "id = ?", pipeline.DocumentID).Error; err != nil {
		return false, fmt.Errorf("load document for pipeline restart: %w", err)
	}
	if document.DeletedAt != nil || document.TenantID != principal.TenantID || document.LatestVersionID != pipeline.DocumentVersionID {
		return false, nil
	}
	var version models.DocumentVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", pipeline.DocumentVersionID).Error; err != nil {
		return false, fmt.Errorf("load version for pipeline restart: %w", err)
	}
	ready, err := s.versionReady(ctx, version)
	if err != nil {
		return false, err
	}
	var stages []models.StageRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", pipeline.ID).Find(&stages).Error; err != nil {
		return false, fmt.Errorf("load stages for pipeline restart: %w", err)
	}
	byStage := make(map[string]models.StageRun, len(stages))
	for _, stage := range stages {
		byStage[stage.Stage] = stage
	}
	target := restartTargetStage(stages)
	if target == "" {
		return false, nil
	}
	now := time.Now()
	// The evidence package survived the delete, so the document is already
	// searchable. The pipeline can still show CANCELLED when the delete landed
	// between the index transaction and its stage completion; settle the records
	// instead of re-running work that already happened.
	if ready {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, stage := range stageOrder {
				current, ok := byStage[stage]
				if !ok {
					continue
				}
				if current.Status != StageSucceeded {
					if err := tx.Model(&models.StageRun{}).Where("id = ?", current.ID).Updates(map[string]any{
						"status": StageSucceeded, "error_code": "", "error_message": "",
						"completed_at": &now, "updated_at": now,
					}).Error; err != nil {
						return err
					}
				}
				if err := tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", current.IdempotencyKey).Updates(map[string]any{
					"status": inboxSucceeded, "last_error": "", "processed_at": &now, "lease_owner": "", "lease_until": nil, "updated_at": now,
				}).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&models.PipelineRun{}).Where("id = ?", pipeline.ID).Updates(statusFields(StatusEvidenceReady, map[string]any{"updated_at": now})).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(statusFields(StatusEvidenceReady, map[string]any{"updated_at": now})).Error; err != nil {
				return err
			}
			return tx.Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{"status": StatusEvidenceReady, "updated_at": now}).Error
		})
		if err != nil {
			return false, fmt.Errorf("finish restored pipeline: %w", err)
		}
		return true, nil
	}
	predecessor := StatusBeforeStage(target)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rewinding := false
		for _, stage := range stageOrder {
			if stage == target {
				rewinding = true
			}
			if !rewinding {
				continue
			}
			current, ok := byStage[stage]
			if !ok {
				continue
			}
			if err := tx.Model(&models.StageRun{}).Where("id = ?", current.ID).Updates(map[string]any{
				"status": StagePending, "attempts": 0, "error_code": "", "error_message": "",
				"started_at": nil, "completed_at": nil, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			// A task that ran while the document was deleted returned early and
			// recorded the inbox row as SUCCEEDED. The stage itself never ran, so
			// the row must stop blocking the restarted task.
			if err := tx.Model(&models.TaskInbox{}).Where("idempotency_key = ?", current.IdempotencyKey).Updates(map[string]any{
				"status": StagePending, "last_error": "", "processed_at": nil, "lease_owner": "", "lease_until": nil, "updated_at": now,
			}).Error; err != nil {
				return err
			}
		}
		pipelineFields := statusFields(predecessor, map[string]any{"updated_at": now})
		if err := tx.Model(&models.PipelineRun{}).Where("id = ?", pipeline.ID).Updates(pipelineFields).Error; err != nil {
			return err
		}
		versionFields := statusFields(predecessor, map[string]any{"updated_at": now})
		if err := tx.Model(&models.DocumentVersion{}).Where("id = ?", version.ID).Updates(versionFields).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Document{}).Where("id = ?", document.ID).Updates(map[string]any{"status": StatusStored, "updated_at": now}).Error; err != nil {
			return err
		}
		targetStage, ok := byStage[target]
		if !ok {
			targetStage = models.StageRun{PipelineRunID: pipeline.ID, Stage: target, Status: StagePending, IdempotencyKey: pipeline.ID + ":" + target}
			if err := tx.Create(&targetStage).Error; err != nil {
				return err
			}
		}
		task := s.taskFor(principal, document, version, pipeline, targetStage, target)
		payload, err := json.Marshal(task)
		if err != nil {
			return fmt.Errorf("marshal restarted task: %w", err)
		}
		var event models.OutboxEvent
		findErr := tx.Where("idempotency_key = ?", targetStage.IdempotencyKey).First(&event).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return tx.Create(&models.OutboxEvent{
				TenantID: pipeline.TenantID, AggregateType: "pipeline_run", AggregateID: pipeline.ID,
				EventType: "document." + target, IdempotencyKey: targetStage.IdempotencyKey, Payload: string(payload),
			}).Error
		}
		if findErr != nil {
			return findErr
		}
		return tx.Model(&event).Updates(map[string]any{
			"payload": string(payload), "published_at": nil, "attempts": 0, "next_attempt_at": nil, "last_error": "",
		}).Error
	})
	if err != nil {
		return false, fmt.Errorf("restart cancelled pipeline: %w", err)
	}
	return true, nil
}

func cancelDocumentPipelines(tx *gorm.DB, documentID string) error {
	now := time.Now()
	var pipelines []models.PipelineRun
	if err := tx.Where("document_id = ? AND status NOT IN ?", documentID, []string{StatusEvidenceReady, StatusCancelled, StatusFailed}).Find(&pipelines).Error; err != nil {
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
	return tx.Model(&models.PipelineRun{}).Where("id IN ?", pipelineIDs).Updates(map[string]any{"status": StatusCancelled, "error_code": "deleted", "error_message": "document deleted", "updated_at": now}).Error
}
