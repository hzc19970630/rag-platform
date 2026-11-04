package ingestion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/formats"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

var filenamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type Service struct {
	db        *gorm.DB
	authz     *authz.Service
	objects   ObjectStore
	publisher TaskPublisher
	maxBytes  int64
	retention time.Duration
	quota     Quota
	index     EvidenceIndexWriter
}

// SetIndexWriter wires the Evidence Index write port. It is optional at
// construction because the API process only needs it for purge; the worker
// injects it before processing stages.
func (s *Service) SetIndexWriter(writer EvidenceIndexWriter) {
	s.index = writer
}

// Quota is the tenant-level ceiling for durable content. Zero disables a limit.
type Quota struct {
	MaxDocuments    int
	MaxStorageBytes int64
}

type UploadInput struct {
	KnowledgeBaseID string
	DocumentID      string
	Filename        string
	MimeType        string
	Content         []byte
	IdempotencyKey  string
	RequestID       string
	TraceID         string
	ActorID         string
}

type UploadResult struct {
	Document models.Document        `json:"document"`
	Version  models.DocumentVersion `json:"version"`
	Pipeline models.PipelineRun     `json:"pipeline"`
	Stages   []models.StageRun      `json:"stages"`
}

// ExternalUploadInput is the connector-facing form of an upload. The caller is
// a background sync run, so there is no interactive principal; the connector's
// knowledge-base binding and tenant are rechecked here and in the datasource
// service, while the resulting document follows the exact same version,
// pipeline and outbox path as a user upload.
type ExternalUploadInput struct {
	TenantID        string
	KnowledgeBaseID string
	DocumentID      string
	Filename        string
	MimeType        string
	Content         []byte
	IdempotencyKey  string
	RequestID       string
	TraceID         string
	ActorID         string
}

type DocumentView struct {
	Document models.Document        `json:"document"`
	Version  models.DocumentVersion `json:"version"`
	Pipeline models.PipelineRun     `json:"pipeline"`
	Stages   []models.StageRun      `json:"stages"`
}

func NewService(db *gorm.DB, authorizer *authz.Service, objects ObjectStore, publisher TaskPublisher, maxBytes int64, retentionDays int, quota Quota) *Service {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	return &Service{db: db, authz: authorizer, objects: objects, publisher: publisher, maxBytes: maxBytes, retention: time.Duration(retentionDays) * 24 * time.Hour, quota: quota}
}

// checkQuota reads tenant usage. Deleted documents do not count: their space is
// either purgeable or still inside the retention window. The early caller is a
// fast-fail; the upload transaction calls it again under lock as the
// authoritative check.
func (s *Service) checkQuota(ctx context.Context, db *gorm.DB, tenantID string, additionalBytes int64) error {
	if s.quota.MaxDocuments <= 0 && s.quota.MaxStorageBytes <= 0 {
		return nil
	}
	var stats struct {
		Documents int64
		Bytes     int64
	}
	query := `SELECT
	    (SELECT COUNT(*) FROM documents d WHERE d.tenant_id = ? AND d.deleted_at IS NULL) AS documents,
	    (SELECT COALESCE(SUM(v.size_bytes), 0) FROM document_versions v JOIN documents d ON d.id = v.document_id WHERE d.tenant_id = ? AND d.deleted_at IS NULL) AS bytes`
	if err := db.WithContext(ctx).Raw(query, tenantID, tenantID).Scan(&stats).Error; err != nil {
		return fmt.Errorf("check tenant quota: %w", err)
	}
	if s.quota.MaxDocuments > 0 && stats.Documents >= int64(s.quota.MaxDocuments) {
		return fmt.Errorf("%w: the tenant document limit of %d has been reached", apperror.ErrQuotaExceeded, s.quota.MaxDocuments)
	}
	if s.quota.MaxStorageBytes > 0 && stats.Bytes+additionalBytes > s.quota.MaxStorageBytes {
		return fmt.Errorf("%w: the tenant storage limit has been reached", apperror.ErrQuotaExceeded)
	}
	return nil
}

// lockTenantQuota serializes quota checks for one tenant. PostgreSQL advisory
// locks are transaction-scoped; SQLite already serializes writers, so its
// development/test dialect needs no extra lock.
func lockTenantQuota(tx *gorm.DB, tenantID string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", tenantID).Error; err != nil {
		return fmt.Errorf("lock tenant quota: %w", err)
	}
	return nil
}

func (s *Service) Upload(ctx context.Context, principal authz.Principal, input UploadInput) (UploadResult, error) {
	if err := s.validateUpload(input); err != nil {
		return UploadResult{}, err
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		input.IdempotencyKey = "upload:" + principal.TenantID + ":" + input.KnowledgeBaseID + ":" + contentHash(input.Content)
	}
	knowledgeBase, err := s.authorizedKnowledgeBase(ctx, principal, input.KnowledgeBaseID, "write")
	if err != nil {
		return UploadResult{}, err
	}
	return s.storeVersion(ctx, principal, knowledgeBase, input, false, false)
}

func (s *Service) IngestExternal(ctx context.Context, input ExternalUploadInput) (UploadResult, error) {
	if strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.KnowledgeBaseID) == "" || strings.TrimSpace(input.DocumentID) == "" || strings.TrimSpace(input.ActorID) == "" {
		return UploadResult{}, fmt.Errorf("connector tenant, knowledge base, document and actor are required")
	}
	upload := UploadInput{
		KnowledgeBaseID: input.KnowledgeBaseID, DocumentID: input.DocumentID, Filename: input.Filename,
		MimeType: input.MimeType, Content: input.Content, IdempotencyKey: input.IdempotencyKey,
		RequestID: input.RequestID, TraceID: input.TraceID, ActorID: input.ActorID,
	}
	if err := s.validateUpload(upload); err != nil {
		return UploadResult{}, err
	}
	if strings.TrimSpace(upload.IdempotencyKey) == "" {
		upload.IdempotencyKey = "connector:" + input.ActorID + ":" + contentHash(input.Content)
	}
	var knowledgeBase models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", input.TenantID, input.KnowledgeBaseID).First(&knowledgeBase).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return UploadResult{}, apperror.ErrNotFound
		}
		return UploadResult{}, fmt.Errorf("load connector knowledge base: %w", err)
	}
	principal := authz.Principal{SubjectID: input.ActorID, TenantID: input.TenantID, CredentialType: "connector"}
	return s.storeVersion(ctx, principal, knowledgeBase, upload, true, true)
}

func (s *Service) validateUpload(input UploadInput) error {
	if len(input.Content) == 0 {
		return fmt.Errorf("file is required")
	}
	if s.maxBytes > 0 && int64(len(input.Content)) > s.maxBytes {
		return fmt.Errorf("file exceeds maximum size")
	}
	if strings.TrimSpace(input.Filename) == "" {
		return fmt.Errorf("filename is required")
	}
	if _, ok := supportedMime(input.Filename, input.MimeType); !ok {
		return fmt.Errorf("unsupported mime type")
	}
	return nil
}

// storeVersion is the single write path for user uploads and connector syncs.
// Keeping one implementation is what guarantees a connector cannot skip
// document versioning, the ingestion pipeline, quota, or the outbox.
// createMissing lets a connector supply a deterministic document ID before the
// row exists; interactive uploads keep the stricter "document must exist"
// behavior when a caller names an ID.
func (s *Service) storeVersion(ctx context.Context, principal authz.Principal, knowledgeBase models.KnowledgeBase, input UploadInput, restoreDeleted, createMissing bool) (UploadResult, error) {
	mimeType, _ := supportedMime(input.Filename, input.MimeType)
	if strings.TrimSpace(input.RequestID) == "" {
		input.RequestID = input.IdempotencyKey
	}
	if strings.TrimSpace(input.TraceID) == "" {
		input.TraceID = input.RequestID
	}

	var existing models.PipelineRun
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND idempotency_key = ?", principal.TenantID, input.IdempotencyKey).First(&existing).Error; err == nil {
		if restoreDeleted {
			if err := s.clearTombstone(ctx, principal.TenantID, existing.DocumentID); err != nil {
				return UploadResult{}, err
			}
			if _, err := s.restartDeletionCancelledPipeline(ctx, principal, existing.ID); err != nil {
				return UploadResult{}, err
			}
		}
		return s.loadResult(ctx, existing.DocumentID, existing.DocumentVersionID, existing.ID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return UploadResult{}, fmt.Errorf("check upload idempotency: %w", err)
	}
	if err := s.checkQuota(ctx, s.db, principal.TenantID, int64(len(input.Content))); err != nil {
		return UploadResult{}, err
	}

	hash := contentHash(input.Content)
	documentID := input.DocumentID
	var document models.Document
	isNewDocument := documentID == ""
	if !isNewDocument {
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", principal.TenantID, input.KnowledgeBaseID, documentID).First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) && createMissing {
				document = models.Document{
					ID: documentID, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID,
					OwnerID: principal.SubjectID, Filename: input.Filename, MimeType: mimeType, Status: StatusReceived,
				}
				isNewDocument = true
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				return UploadResult{}, apperror.ErrNotFound
			} else {
				return UploadResult{}, fmt.Errorf("load document: %w", err)
			}
		}
	}
	if !isNewDocument {
		if restoreDeleted && document.DeletedAt != nil {
			if err := s.clearTombstone(ctx, principal.TenantID, document.ID); err != nil {
				return UploadResult{}, err
			}
			document.DeletedAt = nil
			document.DeletedBy = ""
			document.PurgeAfter = nil
			document.Status = StatusStored
		}
		var duplicate models.DocumentVersion
		if err := s.db.WithContext(ctx).Where("document_id = ? AND content_hash = ?", documentID, hash).First(&duplicate).Error; err == nil {
			var pipeline models.PipelineRun
			if err := s.db.WithContext(ctx).Where("document_version_id = ?", duplicate.ID).Order("created_at DESC").First(&pipeline).Error; err != nil {
				return UploadResult{}, fmt.Errorf("load duplicate pipeline: %w", err)
			}
			if restoreDeleted {
				if _, err := s.restartDeletionCancelledPipeline(ctx, principal, pipeline.ID); err != nil {
					return UploadResult{}, err
				}
			}
			return s.loadResult(ctx, duplicate.DocumentID, duplicate.ID, pipeline.ID)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return UploadResult{}, fmt.Errorf("check document duplicate: %w", err)
		}
	} else if documentID == "" {
		var err error
		documentID, err = newID("doc")
		if err != nil {
			return UploadResult{}, err
		}
		document = models.Document{ID: documentID, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, OwnerID: principal.SubjectID, Filename: input.Filename, MimeType: mimeType, Status: StatusReceived}
	}

	versionID, err := newID("ver")
	if err != nil {
		return UploadResult{}, err
	}
	pipelineID, err := newID("pipe")
	if err != nil {
		return UploadResult{}, err
	}
	stageKey := pipelineID + ":" + StageParse
	objectKey := objectKeyFor(principal.TenantID, input.KnowledgeBaseID, documentID, versionID, input.Filename)
	if err := s.objects.Put(ctx, objectKey, input.Content, mimeType); err != nil {
		return UploadResult{}, err
	}
	cleanupObject := true
	defer func() {
		if cleanupObject {
			_ = s.objects.Delete(context.Background(), objectKey)
		}
	}()

	version := models.DocumentVersion{ID: versionID, DocumentID: documentID, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, Version: 1, ContentHash: hash, SizeBytes: int64(len(input.Content)), SourceObjectKey: objectKey, Status: StatusStored, ACLVersion: knowledgeBase.ACLVersion}
	pipeline := models.PipelineRun{ID: pipelineID, PipelineType: PipelineTypeEvidence, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, DocumentID: documentID, DocumentVersionID: versionID, RequestID: input.RequestID, TraceID: input.TraceID, IdempotencyKey: input.IdempotencyKey, Status: StatusStored}
	stage := models.StageRun{PipelineRunID: pipelineID, Stage: StageParse, Status: StagePending, IdempotencyKey: stageKey}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The pre-transaction check can race with another upload. Re-check under
		// a tenant-scoped lock so the count and the insert are one serial unit.
		if err := lockTenantQuota(tx, principal.TenantID); err != nil {
			return err
		}
		if err := s.checkQuota(ctx, tx, principal.TenantID, int64(len(input.Content))); err != nil {
			return err
		}
		if isNewDocument {
			if err := tx.Create(&document).Error; err != nil {
				return err
			}
		} else {
			var currentVersion models.DocumentVersion
			if document.LatestVersionID != "" && tx.First(&currentVersion, "id = ?", document.LatestVersionID).Error == nil {
				if err := tx.Model(&currentVersion).Updates(map[string]any{"status": StatusCancelled, "error_code": "superseded", "error_message": "superseded by a newer document version"}).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.Chunk{}).Where("document_version_id = ?", currentVersion.ID).Update("status", StatusStale).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.EvidencePackage{}).Where("document_version_id = ?", currentVersion.ID).Update("status", StatusStale).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&document).Updates(map[string]any{"filename": input.Filename, "mime_type": mimeType, "status": StatusStored, "latest_version_id": versionID, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Model(&models.DocumentVersion{}).Where("document_id = ?", documentID).Count(&count).Error; err != nil {
			return err
		}
		version.Version = int(count) + 1
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		if err := tx.Model(&document).Updates(map[string]any{"status": StatusStored, "latest_version_id": versionID, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := tx.Create(&pipeline).Error; err != nil {
			return err
		}
		if err := tx.Create(&stage).Error; err != nil {
			return err
		}
		task := s.taskFor(principal, document, version, pipeline, stage, StageParse)
		payload, err := json.Marshal(task)
		if err != nil {
			return fmt.Errorf("marshal parse task: %w", err)
		}
		return tx.Create(&models.OutboxEvent{TenantID: principal.TenantID, AggregateType: "document_version", AggregateID: versionID, EventType: "document.parse", IdempotencyKey: stageKey, Payload: string(payload)}).Error
	})
	if err != nil {
		return UploadResult{}, fmt.Errorf("create ingestion transaction: %w", err)
	}
	cleanupObject = false
	return s.loadResult(ctx, documentID, versionID, pipelineID)
}

// clearTombstone reactivates a document when the same connector item appears
// again. The remote source is authoritative for its own items, so a reappearing
// item restores the existing document instead of creating a duplicate. A
// current version whose evidence survived the delete comes back searchable; one
// without evidence comes back STORED and must be processed again.
func (s *Service) clearTombstone(ctx context.Context, tenantID, documentID string) error {
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load document for restore: %w", err)
	}
	if document.DeletedAt == nil {
		return nil
	}
	status := StatusStored
	if document.LatestVersionID != "" {
		var version models.DocumentVersion
		if err := s.db.WithContext(ctx).First(&version, "id = ?", document.LatestVersionID).Error; err == nil {
			ready, readyErr := s.versionReady(ctx, version)
			if readyErr != nil {
				return readyErr
			}
			if ready {
				status = StatusEvidenceReady
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load document version for restore: %w", err)
		}
	}
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.Document{}).
		Where("tenant_id = ? AND id = ? AND deleted_at IS NOT NULL", tenantID, documentID).
		Updates(map[string]any{"status": status, "deleted_at": nil, "deleted_by": "", "purge_after": nil, "restored_at": &now, "updated_at": now}).Error
}

func (s *Service) Get(ctx context.Context, principal authz.Principal, documentID string) (DocumentView, error) {
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", principal.TenantID, documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DocumentView{}, apperror.ErrNotFound
		}
		return DocumentView{}, fmt.Errorf("load document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "read"); err != nil {
		return DocumentView{}, err
	}
	if document.LatestVersionID == "" {
		return DocumentView{Document: document}, nil
	}
	var version models.DocumentVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", document.LatestVersionID).Error; err != nil {
		return DocumentView{}, fmt.Errorf("load document version: %w", err)
	}
	var pipeline models.PipelineRun
	if err := s.db.WithContext(ctx).Where("document_version_id = ?", version.ID).Order("created_at DESC").First(&pipeline).Error; err != nil {
		return DocumentView{}, fmt.Errorf("load pipeline: %w", err)
	}
	var stages []models.StageRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", pipeline.ID).Order("id").Find(&stages).Error; err != nil {
		return DocumentView{}, fmt.Errorf("load stages: %w", err)
	}
	return DocumentView{Document: document, Version: version, Pipeline: pipeline, Stages: stages}, nil
}

func (s *Service) authorizedKnowledgeBase(ctx context.Context, principal authz.Principal, id, action string) (models.KnowledgeBase, error) {
	if _, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{KnowledgeBaseID: id}); err != nil {
		return models.KnowledgeBase{}, err
	}
	allowed, err := s.authz.Authorize(ctx, principal, action, "/knowledge-bases/"+id)
	if err != nil {
		return models.KnowledgeBase{}, err
	}
	if !allowed {
		return models.KnowledgeBase{}, apperror.ErrForbidden
	}
	var knowledgeBase models.KnowledgeBase
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, id).First(&knowledgeBase).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.KnowledgeBase{}, apperror.ErrNotFound
		}
		return models.KnowledgeBase{}, fmt.Errorf("load knowledge base: %w", err)
	}
	return knowledgeBase, nil
}

func (s *Service) taskFor(principal authz.Principal, document models.Document, version models.DocumentVersion, pipeline models.PipelineRun, stage models.StageRun, stageName string) Task {
	return Task{Envelope: Envelope{SchemaVersion: SchemaTask, TenantID: principal.TenantID, KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID, PipelineRunID: pipeline.ID, StageRunID: fmt.Sprint(stage.ID), TraceID: pipeline.TraceID, RequestID: pipeline.RequestID, ActorID: principal.SubjectID, IdempotencyKey: stage.IdempotencyKey, ContentHash: version.ContentHash, CreatedAt: time.Now().UTC()}, TaskType: "document.ingestion.v1", Stage: stageName, ObjectKey: version.SourceObjectKey, PackageID: stableID("pkg", version.ID)}
}

func (s *Service) loadResult(ctx context.Context, documentID, versionID, pipelineID string) (UploadResult, error) {
	var document models.Document
	var version models.DocumentVersion
	var pipeline models.PipelineRun
	if err := s.db.WithContext(ctx).First(&document, "id = ?", documentID).Error; err != nil {
		return UploadResult{}, err
	}
	if err := s.db.WithContext(ctx).First(&version, "id = ?", versionID).Error; err != nil {
		return UploadResult{}, err
	}
	if err := s.db.WithContext(ctx).First(&pipeline, "id = ?", pipelineID).Error; err != nil {
		return UploadResult{}, err
	}
	var stages []models.StageRun
	if err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", pipeline.ID).Order("id").Find(&stages).Error; err != nil {
		return UploadResult{}, err
	}
	return UploadResult{Document: document, Version: version, Pipeline: pipeline, Stages: stages}, nil
}

func supportedMime(filename, mimeType string) (string, bool) {
	return formats.Canonicalize(filename, mimeType)
}

func objectKeyFor(tenantID, knowledgeBaseID, documentID, versionID, filename string) string {
	clean := filenamePattern.ReplaceAllString(path.Base(filename), "_")
	return fmt.Sprintf("tenants/%s/knowledge-bases/%s/documents/%s/versions/%s/source/%s", tenantID, knowledgeBaseID, documentID, versionID, clean)
}

func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func newID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}
