package ingestion

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type memoryObjects struct {
	mu        sync.Mutex
	data      map[string][]byte
	getCalls  int
	openCalls int
}

func (m *memoryObjects) Put(_ context.Context, key string, content []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), content...)
	return nil
}

func (m *memoryObjects) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	content, ok := m.data[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return content, nil
}

func (m *memoryObjects) Stat(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	content, ok := m.data[key]
	if !ok {
		return 0, errors.New("object not found")
	}
	return int64(len(content)), nil
}

func (m *memoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openCalls++
	content, ok := m.data[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (m *memoryObjects) counts() (gets, opens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls, m.openCalls
}

func (m *memoryObjects) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func newReparseFixture(t *testing.T) (*Service, *gorm.DB, *memoryObjects) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.AuthorizationRevision{},
		&models.AuditLog{}, &models.Chunk{},
	); err != nil {
		t.Fatalf("migrate reparse tables: %v", err)
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
	content := []byte("reparse idempotency fixture")
	if err := db.Create(&models.Document{
		ID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "user-a",
		Filename: "doc.txt", MimeType: "text/plain", Status: StatusEvidenceReady,
		LatestVersionID: "ver-1", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-1", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		Version: 1, ContentHash: contentHash(content), SizeBytes: int64(len(content)),
		SourceObjectKey: "source-key", Status: StatusEvidenceReady, ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	objects := &memoryObjects{data: map[string][]byte{"source-key": content}}
	return NewService(db, authorizer, objects, nil, 1<<20, 30, Quota{}), db, objects
}

// Retried reparse requests must reuse the run they already started. Before the
// fix every call allocated a new version and pipeline, so three retries left
// three runs behind.
func TestReparseReusesExistingRun(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
	ctx := context.Background()

	var first UploadResult
	for attempt := 0; attempt < 3; attempt++ {
		result, err := service.Reparse(ctx, principal, "doc-a")
		if err != nil {
			t.Fatalf("reparse attempt %d: %v", attempt, err)
		}
		if attempt == 0 {
			first = result
			continue
		}
		if result.Pipeline.ID != first.Pipeline.ID || result.Version.ID != first.Version.ID {
			t.Fatalf("attempt %d created a new run: pipeline=%s version=%s, want %s/%s",
				attempt, result.Pipeline.ID, result.Version.ID, first.Pipeline.ID, first.Version.ID)
		}
	}

	var pipelines int64
	if err := db.Model(&models.PipelineRun{}).Where("document_id = ?", "doc-a").Count(&pipelines).Error; err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if pipelines != 1 {
		t.Fatalf("three reparses produced %d pipeline runs, want 1", pipelines)
	}
	var versions int64
	if err := db.Model(&models.DocumentVersion{}).Where("document_id = ?", "doc-a").Count(&versions).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 2 {
		t.Fatalf("three reparses produced %d versions, want 2 (source + target)", versions)
	}
	var outbox int64
	if err := db.Model(&models.OutboxEvent{}).Where("aggregate_id = ?", first.Version.ID).Count(&outbox).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outbox != 1 {
		t.Fatalf("reparse emitted %d outbox events, want 1", outbox)
	}
	wantKey := "reparse:doc-a:" + first.Version.ID + ":" + StageParse
	if first.Pipeline.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key = %q, want %q", first.Pipeline.IdempotencyKey, wantKey)
	}
}

// Concurrent retries race between the lookup and the insert. The unique
// pipeline key plus the in-transaction recheck must still leave one run.
func TestConcurrentReparseCreatesOneRun(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
	ctx := context.Background()

	const workers = 4
	results := make(chan UploadResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Reparse(ctx, principal, "doc-a")
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent reparse: %v", err)
	}
	var pipelineID, versionID string
	for result := range results {
		if pipelineID == "" {
			pipelineID, versionID = result.Pipeline.ID, result.Version.ID
			continue
		}
		if result.Pipeline.ID != pipelineID || result.Version.ID != versionID {
			t.Fatalf("concurrent retry created a new run: pipeline=%s version=%s, want %s/%s",
				result.Pipeline.ID, result.Version.ID, pipelineID, versionID)
		}
	}
	var pipelines int64
	if err := db.Model(&models.PipelineRun{}).Where("document_id = ?", "doc-a").Count(&pipelines).Error; err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if pipelines != 1 {
		t.Fatalf("concurrent reparses produced %d pipeline runs, want 1", pipelines)
	}
}
