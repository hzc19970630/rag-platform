package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	uploadStatusPending    = "pending"
	uploadStatusUploading  = "uploading"
	uploadStatusCompleting = "completing"
	uploadStatusCompleted  = "completed"
	uploadStatusAborted    = "aborted"
	uploadStatusExpired    = "expired"

	uploadTransportDirect = "direct"
	uploadTransportProxy  = "proxy"

	defaultUploadPartSize           = 16 << 20
	defaultUploadSessionTTL         = 24 * time.Hour
	defaultUploadPresignTTL         = time.Hour
	defaultUploadMaxActivePerTenant = 20
	defaultUploadMaxActivePerKB     = 10
	// proxyPartConcurrencyPerTenant and proxyPartConcurrencyGlobal keep the
	// proxy transport (bytes through the API) from crowding out interactive work.
	proxyPartConcurrencyPerTenant = 4
	proxyPartConcurrencyGlobal    = 8
	maxUploadParts                = 10000
)

// UploadSupport wires resumable uploads. Multipart is nil in tests and in
// compositions that only use the legacy single-shot endpoint.
type UploadSupport struct {
	Multipart          MultipartStore
	Direct             bool
	PartSize           int64
	PresignTTL         time.Duration
	SessionTTL         time.Duration
	MaxActivePerTenant int
	MaxActivePerKB     int
}

// UploadPart is the transport-neutral view of one part. URL is filled only for
// direct uploads; proxy uploads are sent to the API part endpoint.
type UploadPart struct {
	Number    int
	SizeBytes int64
	Uploaded  bool
	URL       string
}

type UploadSessionResult struct {
	Session models.UploadSession
	Parts   []UploadPart
}

type CreateUploadInput struct {
	KnowledgeBaseID string
	DocumentID      string
	FolderID        string
	Filename        string
	MimeType        string
	SizeBytes       int64
	IdempotencyKey  string
}

// SetUploadSupport enables resumable uploads. Calling it more than once
// replaces the transport, which keeps tests and alternate compositions simple.
func (s *Service) SetUploadSupport(support UploadSupport) {
	if support.PartSize <= 0 {
		support.PartSize = defaultUploadPartSize
	}
	if support.PresignTTL <= 0 {
		support.PresignTTL = defaultUploadPresignTTL
	}
	if support.SessionTTL <= 0 {
		support.SessionTTL = defaultUploadSessionTTL
	}
	if support.MaxActivePerTenant <= 0 {
		support.MaxActivePerTenant = defaultUploadMaxActivePerTenant
	}
	if support.MaxActivePerKB <= 0 {
		support.MaxActivePerKB = defaultUploadMaxActivePerKB
	}
	s.uploads = support
	s.uploadGate = newUploadGate()
}

func (s *Service) uploadsEnabled() bool {
	return s.uploads.Multipart != nil
}

func (s *Service) uploadTransport() string {
	if s.uploads.Direct {
		return uploadTransportDirect
	}
	return uploadTransportProxy
}

// CreateUploadSession reserves quota and starts a multipart upload before the
// document row exists. The generated document/version ids make the object key
// stable for the whole session, so a retry never targets a different object.
func (s *Service) CreateUploadSession(ctx context.Context, principal authz.Principal, input CreateUploadInput) (UploadSessionResult, error) {
	if !s.uploadsEnabled() {
		return UploadSessionResult{}, fmt.Errorf("resumable uploads are not configured")
	}
	if input.SizeBytes <= 0 {
		return UploadSessionResult{}, apperror.Invalidf("file size is required")
	}
	if s.maxBytes > 0 && input.SizeBytes > s.maxBytes {
		return UploadSessionResult{}, apperror.Invalidf("file exceeds maximum size")
	}
	if strings.TrimSpace(input.Filename) == "" {
		return UploadSessionResult{}, apperror.Invalidf("filename is required")
	}
	mimeType, ok := s.supportedMime(input.Filename, input.MimeType)
	if !ok {
		return UploadSessionResult{}, apperror.Invalidf("unsupported mime type")
	}
	knowledgeBase, err := s.authorizedKnowledgeBase(ctx, principal, input.KnowledgeBaseID, "write")
	if err != nil {
		return UploadSessionResult{}, err
	}
	folderID := strings.TrimSpace(input.FolderID)
	if folderID != "" {
		if _, err := s.loadFolder(ctx, principal.TenantID, input.KnowledgeBaseID, folderID); err != nil {
			return UploadSessionResult{}, err
		}
	}
	documentID := strings.TrimSpace(input.DocumentID)
	isNewDocument := documentID == ""
	if !isNewDocument {
		var document models.Document
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
			Where("knowledge_base_id = ? AND id = ?", input.KnowledgeBaseID, documentID).First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return UploadSessionResult{}, apperror.ErrNotFound
			}
			return UploadSessionResult{}, fmt.Errorf("load document for upload: %w", err)
		}
	}
	partSize := s.uploads.PartSize
	partCount := int((input.SizeBytes + partSize - 1) / partSize)
	if partCount < 1 || partCount > maxUploadParts {
		return UploadSessionResult{}, apperror.Invalidf("file requires too many upload parts")
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		generated, err := newID("upload-session")
		if err != nil {
			return UploadSessionResult{}, err
		}
		idempotencyKey = generated
	}
	if existing, err := s.loadSessionByIdempotencyKey(ctx, principal.TenantID, idempotencyKey); err == nil {
		return s.sessionResult(ctx, principal, existing, true)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return UploadSessionResult{}, err
	}
	if isNewDocument {
		if documentID, err = newID("doc"); err != nil {
			return UploadSessionResult{}, err
		}
	}
	versionID, err := newID("ver")
	if err != nil {
		return UploadSessionResult{}, err
	}
	sessionID, err := newID("upl")
	if err != nil {
		return UploadSessionResult{}, err
	}
	objectKey := objectKeyFor(principal.TenantID, input.KnowledgeBaseID, documentID, versionID, input.Filename)
	uploadID, err := s.uploads.Multipart.NewMultipart(ctx, objectKey, mimeType)
	if err != nil {
		return UploadSessionResult{}, err
	}
	abortMultipart := true
	defer func() {
		if abortMultipart {
			_ = s.uploads.Multipart.AbortMultipart(context.WithoutCancel(ctx), objectKey, uploadID)
		}
	}()
	session := models.UploadSession{
		ID: sessionID, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID,
		DocumentID: documentID, VersionID: versionID, FolderID: folderID,
		Filename: input.Filename, MimeType: mimeType, SizeBytes: input.SizeBytes,
		PartSize: partSize, PartCount: partCount, ObjectKey: objectKey, MultipartUploadID: uploadID,
		Transport: s.uploadTransport(), Status: uploadStatusPending, ReservedBytes: input.SizeBytes,
		IdempotencyKey: idempotencyKey, CreatedBy: principal.SubjectID,
		ExpiresAt: time.Now().UTC().Add(s.uploads.SessionTTL),
	}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := lockTenantQuota(tx, principal.TenantID); err != nil {
			return err
		}
		reserved, err := reservedUploadBytes(tx, principal.TenantID)
		if err != nil {
			return err
		}
		if err := s.checkQuota(ctx, tx, principal.TenantID, input.SizeBytes+reserved); err != nil {
			return err
		}
		if err := s.checkActiveUploadSessions(tx, principal.TenantID, knowledgeBase.ID); err != nil {
			return err
		}
		return tx.Create(&session).Error
	})
	if err != nil {
		// A concurrent create with the same idempotency key wins the unique
		// index; return that session instead of surfacing a constraint error.
		if existing, lookupErr := s.loadSessionByIdempotencyKey(ctx, principal.TenantID, idempotencyKey); lookupErr == nil {
			return s.sessionResult(ctx, principal, existing, true)
		}
		return UploadSessionResult{}, fmt.Errorf("create upload session: %w", err)
	}
	abortMultipart = false
	result, err := s.sessionResult(ctx, principal, session, true)
	if err != nil {
		// URL signing failed after the row was written; do not leave a live
		// multipart upload behind.
		_ = s.uploads.Multipart.AbortMultipart(context.WithoutCancel(ctx), objectKey, uploadID)
		_ = tenantdb.MustScoped(s.db.WithContext(context.WithoutCancel(ctx)), principal.TenantID).
			Model(&models.UploadSession{}).Where("id = ?", session.ID).
			Updates(map[string]any{"status": uploadStatusAborted, "error_code": "presign_failed", "error_message": err.Error(), "updated_at": time.Now().UTC()}).Error
		return UploadSessionResult{}, err
	}
	if err := s.recordWorkspaceAudit(ctx, principal, "create", "upload_session", session.ID,
		fmt.Sprintf("filename=%s size=%d transport=%s", session.Filename, session.SizeBytes, session.Transport)); err != nil {
		return UploadSessionResult{}, err
	}
	return result, nil
}

func (s *Service) GetUploadSession(ctx context.Context, principal authz.Principal, id string) (UploadSessionResult, error) {
	session, err := s.loadUploadSession(ctx, principal, id)
	if err != nil {
		return UploadSessionResult{}, err
	}
	return s.sessionResult(ctx, principal, session, false)
}

// RefreshUploadPartURLs re-signs only the parts that are still missing. Direct
// transport only; proxy parts are PUT to the API and need no signature.
func (s *Service) RefreshUploadPartURLs(ctx context.Context, principal authz.Principal, id string) (UploadSessionResult, error) {
	session, err := s.loadActiveUploadSession(ctx, principal, id)
	if err != nil {
		return UploadSessionResult{}, err
	}
	if session.Transport != uploadTransportDirect {
		return UploadSessionResult{}, apperror.Invalidf("upload session uses proxy transport")
	}
	return s.sessionResult(ctx, principal, session, true)
}

// PutUploadPart streams one proxy part into object storage. The per-session
// gate allows a single part in flight, so two retries cannot interleave writes
// for the same part.
func (s *Service) PutUploadPart(ctx context.Context, principal authz.Principal, id string, part int, body io.Reader, size int64) (models.UploadSession, error) {
	session, err := s.loadActiveUploadSession(ctx, principal, id)
	if err != nil {
		return models.UploadSession{}, err
	}
	if session.Transport != uploadTransportProxy {
		return models.UploadSession{}, apperror.Invalidf("upload session uses direct transport")
	}
	expected, err := sessionPartSize(session, part)
	if err != nil {
		return models.UploadSession{}, err
	}
	if size != expected {
		return models.UploadSession{}, apperror.Invalidf("upload part size mismatch")
	}
	if s.uploadGate == nil {
		return models.UploadSession{}, fmt.Errorf("upload concurrency is not configured")
	}
	release, ok := s.uploadGate.acquire(session.ID, principal.TenantID)
	if !ok {
		return models.UploadSession{}, fmt.Errorf("%w: too many concurrent upload parts", apperror.ErrRateLimited)
	}
	defer release()
	stored, err := s.uploads.Multipart.PutPart(ctx, session.ObjectKey, session.MultipartUploadID, part, body, size)
	if err != nil {
		return models.UploadSession{}, err
	}
	if stored.Size != expected {
		return models.UploadSession{}, apperror.Invalidf("upload part size mismatch")
	}
	if session.Status == uploadStatusPending {
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
			Model(&models.UploadSession{}).Where("id = ? AND status = ?", session.ID, uploadStatusPending).
			Updates(map[string]any{"status": uploadStatusUploading, "updated_at": time.Now().UTC()}).Error; err != nil {
			return models.UploadSession{}, fmt.Errorf("mark upload session uploading: %w", err)
		}
		session.Status = uploadStatusUploading
	}
	return session, nil
}

// AbortUploadSession is idempotent: aborting a completed session keeps the
// document, aborting an already aborted one succeeds, and the multipart upload
// is removed either way.
func (s *Service) AbortUploadSession(ctx context.Context, principal authz.Principal, id string) error {
	var session models.UploadSession
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load upload session: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, session.KnowledgeBaseID, "write"); err != nil {
		return err
	}
	switch session.Status {
	case uploadStatusCompleted, uploadStatusAborted, uploadStatusExpired:
		return nil
	}
	if err := s.uploads.Multipart.AbortMultipart(ctx, session.ObjectKey, session.MultipartUploadID); err != nil {
		return err
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Model(&models.UploadSession{}).Where("upload_sessions.id = ? AND upload_sessions.status IN ?", id, activeUploadStatuses()).
		Updates(map[string]any{
			"status": uploadStatusAborted, "error_code": "aborted", "error_message": "aborted by user",
			"multipart_upload_id": "", "updated_at": time.Now().UTC(),
		}).Error; err != nil {
		return err
	}
	return s.recordWorkspaceAudit(ctx, principal, "delete", "upload_session", session.ID, "aborted by user")
}

// ExpireUploadSessions is the maintenance pass: abort the multipart upload and
// mark the row expired so its quota reservation stops counting. It is
// cross-tenant, so the caller must use the maintenance connection.
func (s *Service) ExpireUploadSessions(ctx context.Context, now time.Time, limit int) (int, error) {
	if !s.uploadsEnabled() {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}
	var sessions []models.UploadSession
	if err := tenantdb.Unscoped(s.db.WithContext(ctx), "upload session expiry sweep").
		Where("status IN ? AND expires_at < ?", activeUploadStatuses(), now.UTC()).
		Order("expires_at").Limit(limit).Find(&sessions).Error; err != nil {
		return 0, fmt.Errorf("list expired upload sessions: %w", err)
	}
	expired := 0
	for _, session := range sessions {
		if err := s.uploads.Multipart.AbortMultipart(ctx, session.ObjectKey, session.MultipartUploadID); err != nil {
			continue
		}
		// A fresh query per row: reusing the list query would carry its WHERE
		// clause into the update and produce UPDATE ... FROM.
		result := tenantdb.Unscoped(s.db.WithContext(ctx), "upload session expiry sweep").
			Model(&models.UploadSession{}).
			Where("id = ? AND status IN ?", session.ID, activeUploadStatuses()).
			Updates(map[string]any{
				"status": uploadStatusExpired, "error_code": "expired", "error_message": "upload session expired",
				"multipart_upload_id": "", "updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return expired, fmt.Errorf("expire upload session %s: %w", session.ID, result.Error)
		}
		expired += int(result.RowsAffected)
	}
	return expired, nil
}

// AbortKnowledgeBaseUploadsTx aborts every active multipart upload belonging to
// a knowledge base that is about to be deleted. The rows stay in the caller's
// transaction until the delete cascades; without this the storage upload would
// outlive the only record of its upload id.
func (s *Service) AbortKnowledgeBaseUploadsTx(ctx context.Context, tx *gorm.DB, tenantID, knowledgeBaseID string) error {
	if !s.uploadsEnabled() {
		return nil
	}
	var sessions []models.UploadSession
	if err := tenantdb.MustScoped(tx.WithContext(ctx), tenantID).
		Where("knowledge_base_id = ? AND status IN ?", knowledgeBaseID, activeUploadStatuses()).
		Find(&sessions).Error; err != nil {
		return fmt.Errorf("list active upload sessions: %w", err)
	}
	var failures []error
	for _, session := range sessions {
		if err := s.uploads.Multipart.AbortMultipart(ctx, session.ObjectKey, session.MultipartUploadID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// UploadSessionStats reports active sessions and their reserved bytes across
// tenants. The worker publishes it as gauges; the caller must use the
// maintenance connection.
func (s *Service) UploadSessionStats(ctx context.Context) (int64, int64, error) {
	var row struct {
		Active   int64
		Reserved int64
	}
	err := tenantdb.Unscoped(s.db.WithContext(ctx), "upload session metrics").Model(&models.UploadSession{}).
		Select("COUNT(*) AS active, COALESCE(SUM(reserved_bytes), 0) AS reserved").
		Where("status IN ?", activeUploadStatuses()).Scan(&row).Error
	if err != nil {
		return 0, 0, fmt.Errorf("upload session stats: %w", err)
	}
	return row.Active, row.Reserved, nil
}

// PurgeUploadSessions deletes terminal sessions older than the cutoff. Active
// sessions are never touched; the caller must use the maintenance connection.
func (s *Service) PurgeUploadSessions(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	result := tenantdb.Unscoped(s.db.WithContext(ctx), "upload session retention purge").
		Where("status IN ? AND updated_at < ?", []string{uploadStatusCompleted, uploadStatusAborted, uploadStatusExpired}, before.UTC()).
		Limit(limit).Delete(&models.UploadSession{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge upload sessions: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) loadSessionByIdempotencyKey(ctx context.Context, tenantID, key string) (models.UploadSession, error) {
	var session models.UploadSession
	err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("idempotency_key = ?", key).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.UploadSession{}, err
	}
	if err != nil {
		return models.UploadSession{}, fmt.Errorf("load upload session by idempotency key: %w", err)
	}
	return session, nil
}

func (s *Service) loadUploadSession(ctx context.Context, principal authz.Principal, id string) (models.UploadSession, error) {
	if !s.uploadsEnabled() {
		return models.UploadSession{}, fmt.Errorf("resumable uploads are not configured")
	}
	var session models.UploadSession
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.UploadSession{}, apperror.ErrNotFound
		}
		return models.UploadSession{}, fmt.Errorf("load upload session: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, session.KnowledgeBaseID, "write"); err != nil {
		return models.UploadSession{}, err
	}
	return session, nil
}

func (s *Service) loadActiveUploadSession(ctx context.Context, principal authz.Principal, id string) (models.UploadSession, error) {
	session, err := s.loadUploadSession(ctx, principal, id)
	if err != nil {
		return models.UploadSession{}, err
	}
	if !isActiveUploadStatus(session.Status) {
		return models.UploadSession{}, apperror.Invalidf("upload session is %s", session.Status)
	}
	if !session.ExpiresAt.After(time.Now().UTC()) {
		_ = s.expireSession(ctx, principal.TenantID, session)
		return models.UploadSession{}, apperror.Invalidf("upload session expired")
	}
	return session, nil
}

func (s *Service) expireSession(ctx context.Context, tenantID string, session models.UploadSession) error {
	if err := s.uploads.Multipart.AbortMultipart(ctx, session.ObjectKey, session.MultipartUploadID); err != nil {
		return err
	}
	return tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.UploadSession{}).
		Where("upload_sessions.id = ? AND upload_sessions.status IN ?", session.ID, activeUploadStatuses()).
		Updates(map[string]any{
			"status": uploadStatusExpired, "error_code": "expired", "error_message": "upload session expired",
			"multipart_upload_id": "", "updated_at": time.Now().UTC(),
		}).Error
}

func (s *Service) sessionResult(ctx context.Context, principal authz.Principal, session models.UploadSession, withURLs bool) (UploadSessionResult, error) {
	uploaded := map[int]int64{}
	if s.uploadsEnabled() && session.MultipartUploadID != "" {
		parts, err := s.uploads.Multipart.ListParts(ctx, session.ObjectKey, session.MultipartUploadID)
		if err != nil && !errors.Is(err, apperror.ErrNotFound) {
			return UploadSessionResult{}, err
		}
		for _, part := range parts {
			uploaded[part.Number] = part.Size
		}
	}
	result := UploadSessionResult{Session: session}
	for number := 1; number <= session.PartCount; number++ {
		expected, err := sessionPartSize(session, number)
		if err != nil {
			return UploadSessionResult{}, err
		}
		part := UploadPart{Number: number, SizeBytes: expected, Uploaded: uploaded[number] == expected}
		if withURLs && !part.Uploaded && session.Transport == uploadTransportDirect && isActiveUploadStatus(session.Status) {
			url, err := s.uploads.Multipart.PresignPart(ctx, session.ObjectKey, session.MultipartUploadID, number, s.uploads.PresignTTL)
			if err != nil {
				return UploadSessionResult{}, err
			}
			part.URL = url
		}
		result.Parts = append(result.Parts, part)
	}
	return result, nil
}

func (s *Service) checkActiveUploadSessions(tx *gorm.DB, tenantID, knowledgeBaseID string) error {
	var tenantCount, knowledgeBaseCount int64
	if err := tx.Model(&models.UploadSession{}).
		Where("tenant_id = ? AND status IN ?", tenantID, activeUploadStatuses()).
		Count(&tenantCount).Error; err != nil {
		return fmt.Errorf("count tenant upload sessions: %w", err)
	}
	if tenantCount >= int64(s.uploads.MaxActivePerTenant) {
		return fmt.Errorf("%w: too many active upload sessions", apperror.ErrRateLimited)
	}
	if err := tx.Model(&models.UploadSession{}).
		Where("knowledge_base_id = ? AND status IN ?", knowledgeBaseID, activeUploadStatuses()).
		Count(&knowledgeBaseCount).Error; err != nil {
		return fmt.Errorf("count knowledge base upload sessions: %w", err)
	}
	if knowledgeBaseCount >= int64(s.uploads.MaxActivePerKB) {
		return fmt.Errorf("%w: too many active upload sessions for this knowledge base", apperror.ErrRateLimited)
	}
	return nil
}

func reservedUploadBytes(db *gorm.DB, tenantID string) (int64, error) {
	var reserved int64
	if err := db.Model(&models.UploadSession{}).
		Where("tenant_id = ? AND status IN ?", tenantID, activeUploadStatuses()).
		Select("COALESCE(SUM(reserved_bytes), 0)").Scan(&reserved).Error; err != nil {
		return 0, fmt.Errorf("sum reserved upload bytes: %w", err)
	}
	return reserved, nil
}

func activeUploadStatuses() []string {
	return []string{uploadStatusPending, uploadStatusUploading, uploadStatusCompleting}
}

func isActiveUploadStatus(status string) bool {
	switch status {
	case uploadStatusPending, uploadStatusUploading, uploadStatusCompleting:
		return true
	}
	return false
}

func sessionPartSize(session models.UploadSession, part int) (int64, error) {
	if part < 1 || part > session.PartCount {
		return 0, apperror.Invalidf("upload part is out of range")
	}
	if part < session.PartCount {
		return session.PartSize, nil
	}
	size := session.SizeBytes - session.PartSize*int64(session.PartCount-1)
	if size <= 0 {
		return 0, apperror.Invalidf("upload part is out of range")
	}
	return size, nil
}

// CompleteUploadSession turns a fully uploaded multipart object into a
// document version through the same registration path as a regular upload.
// It is idempotent: a repeated call replays the stored result, and a retry
// after a crash continues from the already completed object.
func (s *Service) CompleteUploadSession(ctx context.Context, principal authz.Principal, id, requestID, traceID string) (UploadResult, error) {
	if !s.uploadsEnabled() {
		return UploadResult{}, fmt.Errorf("resumable uploads are not configured")
	}
	session, err := s.loadUploadSession(ctx, principal, id)
	if err != nil {
		return UploadResult{}, err
	}
	switch session.Status {
	case uploadStatusCompleted:
		return s.replayCompletedUpload(ctx, principal, session)
	case uploadStatusAborted, uploadStatusExpired:
		return UploadResult{}, apperror.Invalidf("upload session is %s", session.Status)
	case uploadStatusCompleting:
		if result, ok, replayErr := s.replayCompletedUploadIfRegistered(ctx, principal, session); replayErr != nil {
			return UploadResult{}, replayErr
		} else if ok {
			return result, nil
		}
		// A previous attempt died before registration. The object may already
		// be composed, so fall through and continue.
	}
	if !session.ExpiresAt.After(time.Now().UTC()) {
		_ = s.expireSession(ctx, principal.TenantID, session)
		return UploadResult{}, apperror.Invalidf("upload session expired")
	}
	if err := s.markUploadCompleting(ctx, principal.TenantID, session.ID); err != nil {
		return UploadResult{}, err
	}
	session.Status = uploadStatusCompleting
	registered := false
	defer func() {
		if !registered {
			// The reservation must stay live for a retry that has not written
			// a version yet.
			_ = s.resetUploadSession(ctx, principal.TenantID, session.ID)
		}
	}()

	parts, err := s.uploads.Multipart.ListParts(ctx, session.ObjectKey, session.MultipartUploadID)
	if err != nil {
		// A concurrent complete may have already assembled the object. The size
		// check is the authority in that case.
		if !errors.Is(err, apperror.ErrNotFound) {
			return UploadResult{}, err
		}
		size, statErr := s.objects.Stat(ctx, session.ObjectKey)
		if statErr != nil || size != session.SizeBytes {
			return UploadResult{}, fmt.Errorf("complete upload: %w", err)
		}
	} else {
		if err := validateUploadParts(session, parts); err != nil {
			return UploadResult{}, err
		}
		if _, completeErr := s.uploads.Multipart.CompleteMultipart(ctx, session.ObjectKey, session.MultipartUploadID, parts); completeErr != nil {
			if statSize, statErr := s.objects.Stat(ctx, session.ObjectKey); statErr != nil || statSize != session.SizeBytes {
				return UploadResult{}, completeErr
			}
		}
		// S3 returns no object size on CompleteMultipartUpload, so the stored
		// object is the authority for both this and the retry path.
		storedSize, statErr := s.objects.Stat(ctx, session.ObjectKey)
		if statErr != nil {
			return UploadResult{}, fmt.Errorf("verify completed upload: %w", statErr)
		}
		if storedSize != session.SizeBytes {
			return UploadResult{}, apperror.Invalidf("uploaded size does not match the session")
		}
	}
	hash, err := s.hashObject(ctx, session.ObjectKey)
	if err != nil {
		return UploadResult{}, err
	}
	var knowledgeBase models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("id = ?", session.KnowledgeBaseID).First(&knowledgeBase).Error; err != nil {
		return UploadResult{}, fmt.Errorf("load knowledge base for upload: %w", err)
	}
	createDocument := false
	var document models.Document
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("knowledge_base_id = ? AND id = ?", session.KnowledgeBaseID, session.DocumentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			createDocument = true
		} else {
			return UploadResult{}, fmt.Errorf("load document for upload: %w", err)
		}
	}
	pipelineID, err := newID("pipe")
	if err != nil {
		return UploadResult{}, err
	}
	completedAt := time.Now().UTC()
	result, err := s.storeVersion(ctx, principal, knowledgeBase, UploadInput{
		KnowledgeBaseID: session.KnowledgeBaseID,
		DocumentID:      session.DocumentID,
		FolderID:        session.FolderID,
		Filename:        session.Filename,
		MimeType:        session.MimeType,
		StoredObject:    &StoredObject{Key: session.ObjectKey, SizeBytes: session.SizeBytes, ContentHash: hash},
		VersionID:       session.VersionID,
		PipelineID:      pipelineID,
		CreateDocument:  createDocument,
		IdempotencyKey:  "upload-session:" + session.ID,
		RequestID:       requestID,
		TraceID:         traceID,
		ActorID:         principal.SubjectID,
		RegisterStart: func(tx *gorm.DB) error {
			updated := tenantdb.MustScoped(tx, principal.TenantID).Model(&models.UploadSession{}).
				Where("upload_sessions.id = ? AND upload_sessions.status = ?", session.ID, uploadStatusCompleting).
				Updates(map[string]any{
					"status": uploadStatusCompleted, "multipart_upload_id": "", "completed_at": completedAt,
					"result_document_id": session.DocumentID, "result_version_id": session.VersionID,
					"result_pipeline_id": pipelineID, "updated_at": completedAt,
				})
			if updated.Error != nil {
				return fmt.Errorf("settle upload session: %w", updated.Error)
			}
			if updated.RowsAffected == 0 {
				return apperror.Invalidf("upload session is no longer active")
			}
			return nil
		},
	}, false, false)
	if err != nil {
		// A concurrent complete may have registered the version first; replay it
		// rather than reporting a failure for data that exists.
		if replayed, ok, replayErr := s.replayCompletedUploadIfRegistered(ctx, principal, session); replayErr == nil && ok {
			registered = true
			return replayed, nil
		}
		return UploadResult{}, err
	}
	registered = true
	// Early-return paths inside storeVersion (idempotent replay, duplicate
	// content) skip RegisterStart, so settle with the actual result ids.
	if err := s.finalizeUploadSession(ctx, principal.TenantID, session.ID, result); err != nil {
		return UploadResult{}, err
	}
	if err := s.recordWorkspaceAudit(ctx, principal, "write", "upload_session", session.ID,
		fmt.Sprintf("document=%s version=%s", result.Document.ID, result.Version.ID)); err != nil {
		return UploadResult{}, err
	}
	return result, nil
}

func (s *Service) replayCompletedUpload(ctx context.Context, principal authz.Principal, session models.UploadSession) (UploadResult, error) {
	result, err := s.loadResult(ctx, principal.TenantID, session.ResultDocumentID, session.ResultVersionID, session.ResultPipelineID)
	if err != nil {
		return UploadResult{}, err
	}
	return result, nil
}

// replayCompletedUploadIfRegistered finds the pipeline this session already
// registered (by its idempotency key) and settles the row with those ids.
func (s *Service) replayCompletedUploadIfRegistered(ctx context.Context, principal authz.Principal, session models.UploadSession) (UploadResult, bool, error) {
	var pipeline models.PipelineRun
	err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("idempotency_key = ?", "upload-session:"+session.ID).First(&pipeline).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return UploadResult{}, false, nil
	}
	if err != nil {
		return UploadResult{}, false, fmt.Errorf("find completed upload pipeline: %w", err)
	}
	result, err := s.loadResult(ctx, principal.TenantID, pipeline.DocumentID, pipeline.DocumentVersionID, pipeline.ID)
	if err != nil {
		return UploadResult{}, false, err
	}
	if err := s.finalizeUploadSession(ctx, principal.TenantID, session.ID, result); err != nil {
		return UploadResult{}, false, err
	}
	return result, true, nil
}

func (s *Service) finalizeUploadSession(ctx context.Context, tenantID, sessionID string, result UploadResult) error {
	if result.Pipeline.ID == "" {
		return nil
	}
	return tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.UploadSession{}).
		Where("upload_sessions.id = ? AND upload_sessions.status IN ?", sessionID, []string{uploadStatusCompleting, uploadStatusCompleted}).
		Updates(map[string]any{
			"status": uploadStatusCompleted, "multipart_upload_id": "", "completed_at": time.Now().UTC(),
			"result_document_id": result.Document.ID, "result_version_id": result.Version.ID,
			"result_pipeline_id": result.Pipeline.ID, "updated_at": time.Now().UTC(),
		}).Error
}

func (s *Service) markUploadCompleting(ctx context.Context, tenantID, sessionID string) error {
	result := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.UploadSession{}).
		Where("upload_sessions.id = ? AND upload_sessions.status IN ?", sessionID, []string{uploadStatusPending, uploadStatusUploading}).
		Updates(map[string]any{"status": uploadStatusCompleting, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("mark upload session completing: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.Invalidf("upload session is not active")
	}
	return nil
}

func (s *Service) resetUploadSession(ctx context.Context, tenantID, sessionID string) error {
	return tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.UploadSession{}).
		Where("upload_sessions.id = ? AND upload_sessions.status = ?", sessionID, uploadStatusCompleting).
		Updates(map[string]any{"status": uploadStatusUploading, "updated_at": time.Now().UTC()}).Error
}

func (s *Service) hashObject(ctx context.Context, key string) (string, error) {
	reader, err := s.objects.Open(ctx, key)
	if err != nil {
		return "", fmt.Errorf("open uploaded object: %w", err)
	}
	defer reader.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, reader); err != nil {
		return "", fmt.Errorf("hash uploaded object: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateUploadParts(session models.UploadSession, parts []MultipartPart) error {
	if len(parts) != session.PartCount {
		return apperror.Invalidf("upload is missing parts")
	}
	ordered := append([]MultipartPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	for index, part := range ordered {
		expected, err := sessionPartSize(session, index+1)
		if err != nil {
			return err
		}
		if part.Number != index+1 || part.Size != expected {
			return apperror.Invalidf("upload part %d is missing or has the wrong size", index+1)
		}
	}
	return nil
}

// uploadGate bounds proxy part concurrency: one part per session in flight, a
// tenant cap, and a process-wide cap. It is a single-process guard; multi-replica
// coordination remains the B1 rate-limit decision.
type uploadGate struct {
	mu       sync.Mutex
	sessions map[string]struct{}
	tenants  map[string]int
	slots    chan struct{}
}

func newUploadGate() *uploadGate {
	return &uploadGate{
		sessions: map[string]struct{}{},
		tenants:  map[string]int{},
		slots:    make(chan struct{}, proxyPartConcurrencyGlobal),
	}
}

func (g *uploadGate) acquire(sessionID, tenantID string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, busy := g.sessions[sessionID]; busy {
		return nil, false
	}
	if g.tenants[tenantID] >= proxyPartConcurrencyPerTenant {
		return nil, false
	}
	select {
	case g.slots <- struct{}{}:
	default:
		return nil, false
	}
	g.sessions[sessionID] = struct{}{}
	g.tenants[tenantID]++
	return func() {
		g.mu.Lock()
		delete(g.sessions, sessionID)
		g.tenants[tenantID]--
		if g.tenants[tenantID] <= 0 {
			delete(g.tenants, tenantID)
		}
		<-g.slots
		g.mu.Unlock()
	}, true
}
