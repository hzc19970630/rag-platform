package ingestion

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type memoryMultipart struct {
	mu        sync.Mutex
	objects   *memoryObjects
	uploads   map[string]*memoryMultipartUpload
	next      int
	aborted   []string
	completed int
}

type memoryMultipartUpload struct {
	key   string
	parts map[int][]byte
}

func newMemoryMultipart(objects *memoryObjects) *memoryMultipart {
	return &memoryMultipart{objects: objects, uploads: map[string]*memoryMultipartUpload{}}
}

func (m *memoryMultipart) NewMultipart(_ context.Context, key, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	id := "mp-" + string(rune('a'+m.next-1))
	m.uploads[id] = &memoryMultipartUpload{key: key, parts: map[int][]byte{}}
	return id, nil
}

func (m *memoryMultipart) PutPart(_ context.Context, key, uploadID string, part int, body io.Reader, size int64) (MultipartPart, error) {
	content, err := io.ReadAll(body)
	if err != nil {
		return MultipartPart{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	upload, ok := m.uploads[uploadID]
	if !ok || upload.key != key {
		return MultipartPart{}, apperror.ErrNotFound
	}
	if int64(len(content)) != size {
		return MultipartPart{}, apperror.Invalidf("part size mismatch")
	}
	upload.parts[part] = content
	return MultipartPart{Number: part, Size: size, ETag: "etag"}, nil
}

func (m *memoryMultipart) PresignPart(_ context.Context, key, uploadID string, part int, _ time.Duration) (string, error) {
	return "https://storage.example/" + uploadID + "/" + key + "?part=" + string(rune('0'+part)), nil
}

func (m *memoryMultipart) ListParts(_ context.Context, key, uploadID string) ([]MultipartPart, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	upload, ok := m.uploads[uploadID]
	if !ok || upload.key != key {
		return nil, apperror.ErrNotFound
	}
	parts := make([]MultipartPart, 0, len(upload.parts))
	for number, content := range upload.parts {
		parts = append(parts, MultipartPart{Number: number, Size: int64(len(content)), ETag: "etag"})
	}
	return parts, nil
}

func (m *memoryMultipart) CompleteMultipart(_ context.Context, key, uploadID string, _ []MultipartPart) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	upload, ok := m.uploads[uploadID]
	if !ok || upload.key != key {
		return 0, apperror.ErrNotFound
	}
	composed := []byte{}
	for number := 1; number <= len(upload.parts); number++ {
		composed = append(composed, upload.parts[number]...)
	}
	_ = m.objects.Put(context.Background(), key, composed, "text/plain")
	delete(m.uploads, uploadID)
	m.completed++
	return int64(len(composed)), nil
}

func (m *memoryMultipart) AbortMultipart(_ context.Context, key, uploadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aborted = append(m.aborted, uploadID)
	delete(m.uploads, uploadID)
	return nil
}

func (m *memoryMultipart) abortedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.aborted)
}

func newUploadFixture(t *testing.T, support UploadSupport) (*Service, *gorm.DB, *memoryMultipart, authz.Principal) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.AuthorizationRevision{},
		&models.AuditLog{}, &models.UploadSession{}, &models.User{},
	); err != nil {
		t.Fatalf("migrate upload tables: %v", err)
	}
	authorizer, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	now := time.Now()
	if err := db.Create(&models.Membership{TenantID: "tenant-a", UserID: "user-a", Role: "owner", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{ID: "kb-a", TenantID: "tenant-a", OwnerID: "user-a", Name: "KB A", ACLVersion: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	objects := &memoryObjects{data: map[string][]byte{}}
	service := NewService(db, authorizer, objects, nil, 64<<20, 30, Quota{})
	if support.Multipart == nil {
		support.Multipart = newMemoryMultipart(objects)
	}
	service.SetUploadSupport(support)
	return service, db, support.Multipart.(*memoryMultipart), authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
}

func TestCreateUploadSessionReservesQuotaAndSignsParts(t *testing.T) {
	service, db, multipart, principal := newUploadFixture(t, UploadSupport{Direct: true, PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	result, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 10, IdempotencyKey: "key-1",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if result.Session.Transport != uploadTransportDirect || result.Session.PartCount != 3 || result.Session.Status != uploadStatusPending {
		t.Fatalf("session = %+v", result.Session)
	}
	if len(result.Parts) != 3 || result.Parts[0].SizeBytes != 4 || result.Parts[2].SizeBytes != 2 {
		t.Fatalf("parts = %+v", result.Parts)
	}
	for _, part := range result.Parts {
		if part.URL == "" {
			t.Fatalf("direct transport must sign every part: %+v", part)
		}
	}
	var stored models.UploadSession
	if err := db.First(&stored, "id = ?", result.Session.ID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if stored.ReservedBytes != 10 || stored.MultipartUploadID == "" {
		t.Fatalf("stored session = %+v", stored)
	}
	if multipart.next != 1 {
		t.Fatalf("multipart uploads = %d, want 1", multipart.next)
	}
}

func TestCreateUploadSessionIsIdempotent(t *testing.T) {
	service, _, multipart, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	input := CreateUploadInput{KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "same-key"}
	first, err := service.CreateUploadSession(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := service.CreateUploadSession(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.Session.ID != second.Session.ID {
		t.Fatalf("idempotent create returned %s then %s", first.Session.ID, second.Session.ID)
	}
	if multipart.next != 1 {
		t.Fatalf("multipart uploads = %d, want 1", multipart.next)
	}
}

func TestCreateUploadSessionEnforcesActiveLimit(t *testing.T) {
	service, _, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 1, MaxActivePerKB: 1})
	if _, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "one",
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "b.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "two",
	})
	if !errors.Is(err, apperror.ErrRateLimited) {
		t.Fatalf("second create error = %v, want rate limited", err)
	}
}

func TestPutUploadPartValidatesSizeAndReportsProgress(t *testing.T) {
	service, _, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	created, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "parts",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Session.Transport != uploadTransportProxy {
		t.Fatalf("transport = %s, want proxy", created.Session.Transport)
	}
	if _, err := service.PutUploadPart(context.Background(), principal, created.Session.ID, 1, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("put part: %v", err)
	}
	if _, err := service.PutUploadPart(context.Background(), principal, created.Session.ID, 1, bytes.NewReader([]byte("ab")), 2); err == nil {
		t.Fatal("a part with the wrong size must be rejected")
	}
	status, err := service.GetUploadSession(context.Background(), principal, created.Session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !status.Parts[0].Uploaded || status.Parts[1].Uploaded {
		t.Fatalf("parts after one upload = %+v", status.Parts)
	}
	if status.Session.Status != uploadStatusUploading {
		t.Fatalf("status = %s, want uploading", status.Session.Status)
	}
}

func TestAbortUploadSessionIsIdempotent(t *testing.T) {
	service, _, multipart, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	created, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "abort",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.AbortUploadSession(context.Background(), principal, created.Session.ID); err != nil {
			t.Fatalf("abort attempt %d: %v", attempt, err)
		}
	}
	if multipart.abortedCount() != 1 {
		t.Fatalf("aborted calls = %d, want 1", multipart.abortedCount())
	}
	status, err := service.GetUploadSession(context.Background(), principal, created.Session.ID)
	if err != nil {
		t.Fatalf("get aborted session: %v", err)
	}
	if status.Session.Status != uploadStatusAborted {
		t.Fatalf("status = %s, want aborted", status.Session.Status)
	}
}

func TestExpireUploadSessionsAbortsAndReleasesSlot(t *testing.T) {
	service, db, multipart, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 1, MaxActivePerKB: 1})
	created, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "expire",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&models.UploadSession{}).Where("id = ?", created.Session.ID).Update("expires_at", past).Error; err != nil {
		t.Fatalf("age session: %v", err)
	}
	expired, err := service.ExpireUploadSessions(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if expired != 1 || multipart.abortedCount() != 1 {
		t.Fatalf("expired = %d, aborted = %d", expired, multipart.abortedCount())
	}
	// The reservation no longer counts, so the next session fits the same cap.
	if _, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "b.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "after-expire",
	}); err != nil {
		t.Fatalf("create after expiry: %v", err)
	}
}

func TestCompleteUploadSessionRegistersOnceAndIsIdempotent(t *testing.T) {
	service, db, multipart, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	ctx := context.Background()
	created, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "complete",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, created.Session.ID, 1, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("part 1: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, created.Session.ID, 2, bytes.NewReader([]byte("ef")), 2); err != nil {
		t.Fatalf("part 2: %v", err)
	}
	result, err := service.CompleteUploadSession(ctx, principal, created.Session.ID, "req-1", "trace-1")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result.Document.ID != created.Session.DocumentID || result.Version.ID != created.Session.VersionID {
		t.Fatalf("registered ids = %s/%s, session ids = %s/%s", result.Document.ID, result.Version.ID, created.Session.DocumentID, created.Session.VersionID)
	}
	if result.Version.SizeBytes != 6 {
		t.Fatalf("version size = %d", result.Version.SizeBytes)
	}
	var stored models.UploadSession
	if err := db.First(&stored, "id = ?", created.Session.ID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if stored.Status != uploadStatusCompleted || stored.ResultPipelineID != result.Pipeline.ID || stored.MultipartUploadID != "" {
		t.Fatalf("settled session = %+v", stored)
	}
	var outbox, documents int64
	db.Model(&models.OutboxEvent{}).Count(&outbox)
	db.Model(&models.Document{}).Count(&documents)
	if outbox != 1 || documents != 1 {
		t.Fatalf("outbox=%d documents=%d", outbox, documents)
	}
	if content := multipart.objects.data[stored.ObjectKey]; string(content) != "abcdef" {
		t.Fatalf("composed object = %q", content)
	}

	replayed, err := service.CompleteUploadSession(ctx, principal, created.Session.ID, "req-2", "trace-2")
	if err != nil {
		t.Fatalf("replay complete: %v", err)
	}
	if replayed.Version.ID != result.Version.ID {
		t.Fatalf("replay version = %s, want %s", replayed.Version.ID, result.Version.ID)
	}
	db.Model(&models.OutboxEvent{}).Count(&outbox)
	if outbox != 1 {
		t.Fatalf("replay enqueued again: outbox=%d", outbox)
	}
}

func TestCompleteUploadSessionRejectsMissingPartsAndKeepsReservation(t *testing.T) {
	service, db, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	ctx := context.Background()
	created, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "incomplete",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, created.Session.ID, 1, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("part 1: %v", err)
	}
	if _, err := service.CompleteUploadSession(ctx, principal, created.Session.ID, "", ""); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("complete error = %v, want missing parts", err)
	}
	var stored models.UploadSession
	if err := db.First(&stored, "id = ?", created.Session.ID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if stored.Status != uploadStatusUploading {
		t.Fatalf("status = %s, want uploading after failed complete", stored.Status)
	}
	var outbox int64
	db.Model(&models.OutboxEvent{}).Count(&outbox)
	if outbox != 0 {
		t.Fatalf("failed complete enqueued parse: outbox=%d", outbox)
	}
}

func TestCompleteUploadSessionDuplicateContentReusesVersion(t *testing.T) {
	service, _, multipart, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	ctx := context.Background()
	first, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "duplicate-1",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, first.Session.ID, 1, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("first part 1: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, first.Session.ID, 2, bytes.NewReader([]byte("ef")), 2); err != nil {
		t.Fatalf("first part 2: %v", err)
	}
	original, err := service.CompleteUploadSession(ctx, principal, first.Session.ID, "", "")
	if err != nil {
		t.Fatalf("complete first: %v", err)
	}
	replacement, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", DocumentID: original.Document.ID, Filename: "a.txt", MimeType: "text/plain",
		SizeBytes: 6, IdempotencyKey: "duplicate-2",
	})
	if err != nil {
		t.Fatalf("create replacement: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, replacement.Session.ID, 1, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("replacement part 1: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, replacement.Session.ID, 2, bytes.NewReader([]byte("ef")), 2); err != nil {
		t.Fatalf("replacement part 2: %v", err)
	}
	duplicate, err := service.CompleteUploadSession(ctx, principal, replacement.Session.ID, "", "")
	if err != nil {
		t.Fatalf("complete replacement: %v", err)
	}
	if duplicate.Version.ID != original.Version.ID {
		t.Fatalf("duplicate version = %s, want %s", duplicate.Version.ID, original.Version.ID)
	}
	if _, err := multipart.objects.Stat(ctx, replacement.Session.ObjectKey); err == nil {
		t.Fatal("duplicate upload object must be removed")
	}
	get, err := service.GetUploadSession(ctx, principal, replacement.Session.ID)
	if err != nil {
		t.Fatalf("get replacement session: %v", err)
	}
	if get.Session.Status != uploadStatusCompleted || get.Session.ResultVersionID != original.Version.ID {
		t.Fatalf("replacement session = %+v", get.Session)
	}
}

func TestCreateUploadSessionRequiresWritePermission(t *testing.T) {
	service, db, _, _ := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	now := time.Now()
	if err := db.Create(&models.Membership{TenantID: "tenant-a", UserID: "user-b", Role: "member", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed second membership: %v", err)
	}
	_, err := service.CreateUploadSession(context.Background(), authz.Principal{SubjectID: "user-b", TenantID: "tenant-a", CredentialType: "session"}, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 4,
	})
	if !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("create as tenant member error = %v, want forbidden", err)
	}
}

func TestCompleteUploadSessionEnforcesQuotaAtSettlement(t *testing.T) {
	service, db, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	ctx := context.Background()
	created, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "quota",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, created.Session.ID, 1, bytes.NewReader([]byte("abcd")), 4); err != nil {
		t.Fatalf("part 1: %v", err)
	}
	if _, err := service.PutUploadPart(ctx, principal, created.Session.ID, 2, bytes.NewReader([]byte("ef")), 2); err != nil {
		t.Fatalf("part 2: %v", err)
	}
	// The limit drops between reservation and settlement; the version must not
	// be written and the session must stay retryable.
	service.quota = Quota{MaxStorageBytes: 5}
	if _, err := service.CompleteUploadSession(ctx, principal, created.Session.ID, "", ""); err == nil || !strings.Contains(err.Error(), "storage limit") {
		t.Fatalf("complete error = %v, want storage limit", err)
	}
	var stored models.UploadSession
	if err := db.First(&stored, "id = ?", created.Session.ID).Error; err != nil {
		t.Fatalf("load session: %v", err)
	}
	if stored.Status != uploadStatusUploading {
		t.Fatalf("status = %s, want uploading after quota rejection", stored.Status)
	}
	var documents, outbox int64
	db.Model(&models.Document{}).Count(&documents)
	db.Model(&models.OutboxEvent{}).Count(&outbox)
	if documents != 0 || outbox != 0 {
		t.Fatalf("documents=%d outbox=%d after rejected settlement", documents, outbox)
	}
}

func TestAbortKnowledgeBaseUploadsTxStopsActiveMultipart(t *testing.T) {
	service, db, multipart, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	if _, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "kb-delete",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := service.AbortKnowledgeBaseUploadsTx(context.Background(), db, "tenant-a", "kb-a"); err != nil {
		t.Fatalf("abort knowledge base uploads: %v", err)
	}
	if multipart.abortedCount() != 1 {
		t.Fatalf("aborted calls = %d, want 1", multipart.abortedCount())
	}
}

func TestUploadSessionStatsReportsActiveReservations(t *testing.T) {
	service, _, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	created, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", MimeType: "text/plain", SizeBytes: 6, IdempotencyKey: "stats",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	active, reserved, err := service.UploadSessionStats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if active != 1 || reserved != 6 {
		t.Fatalf("stats = %d/%d, want 1/6", active, reserved)
	}
	if err := service.AbortUploadSession(context.Background(), principal, created.Session.ID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	active, reserved, err = service.UploadSessionStats(context.Background())
	if err != nil {
		t.Fatalf("stats after abort: %v", err)
	}
	if active != 0 || reserved != 0 {
		t.Fatalf("stats after abort = %d/%d", active, reserved)
	}
}

func TestPurgeUploadSessionsOnlyRemovesOldTerminalSessions(t *testing.T) {
	service, db, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4, MaxActivePerTenant: 5, MaxActivePerKB: 5})
	ctx := context.Background()
	old, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "old.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "purge-old",
	})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	if err := service.AbortUploadSession(ctx, principal, old.Session.ID); err != nil {
		t.Fatalf("abort old: %v", err)
	}
	past := time.Now().UTC().Add(-48 * time.Hour)
	if err := db.Model(&models.UploadSession{}).Where("id = ?", old.Session.ID).Update("updated_at", past).Error; err != nil {
		t.Fatalf("age old session: %v", err)
	}
	active, err := service.CreateUploadSession(ctx, principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "active.txt", MimeType: "text/plain", SizeBytes: 4, IdempotencyKey: "purge-active",
	})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	purged, err := service.PurgeUploadSessions(ctx, time.Now().UTC().Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
	var remaining int64
	db.Model(&models.UploadSession{}).Count(&remaining)
	if remaining != 1 {
		t.Fatalf("remaining sessions = %d, want active only", remaining)
	}
	if err := db.First(&models.UploadSession{}, "id = ?", active.Session.ID).Error; err != nil {
		t.Fatalf("active session removed: %v", err)
	}
}

func TestCreateUploadSessionRejectsOversizeAndUnsupportedFormat(t *testing.T) {
	service, _, _, principal := newUploadFixture(t, UploadSupport{PartSize: 4})
	_, err := service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.png", SizeBytes: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported mime type") {
		t.Fatalf("unsupported format error = %v", err)
	}
	_, err = service.CreateUploadSession(context.Background(), principal, CreateUploadInput{
		KnowledgeBaseID: "kb-a", Filename: "a.txt", SizeBytes: 65 << 20,
	})
	if err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Fatalf("oversize error = %v", err)
	}
}
