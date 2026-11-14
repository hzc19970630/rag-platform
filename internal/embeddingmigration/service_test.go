package embeddingmigration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type allowAuthz struct{ allowed bool }

func (a allowAuthz) AuthorizeKnowledgeBase(context.Context, authz.Principal, string, string) (bool, error) {
	return a.allowed, nil
}

type fixedCatalog struct {
	binding embedding.Binding
	err     error
}

func (c fixedCatalog) ResolveEmbeddingModel(context.Context, string, string) (embedding.Binding, error) {
	return c.binding, c.err
}

type testEmbedder struct {
	name      string
	dimension int
}

func (e testEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = make([]float32, e.dimension)
	}
	return vectors, nil
}
func (e testEmbedder) Dimension() int { return e.dimension }
func (e testEmbedder) Name() string   { return e.name }

type testFactory struct{ embedder testEmbedder }

func (f testFactory) EmbedderForModel(context.Context, string, string) (embedding.Binding, embedding.Embedder, error) {
	return embedding.Binding{Model: f.embedder.name, Dimension: f.embedder.dimension}, f.embedder, nil
}

type dbWriter struct {
	db     *gorm.DB
	models []string
	rows   int
}

func (w *dbWriter) WriteVariants(_ context.Context, model string, records []VariantRecord) error {
	w.models = append(w.models, model)
	for _, record := range records {
		row := models.ChunkEmbedding{
			ChunkID: record.ChunkID, TenantID: record.TenantID, KnowledgeBaseID: record.KnowledgeBaseID,
			Model: model, Dimension: record.Dimension, Embedding: "vec",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := w.db.Create(&row).Error; err != nil {
			return err
		}
	}
	w.rows += len(records)
	return nil
}

type testIndexer struct{ dimensions []int }

func (i *testIndexer) EnsureIndex(_ context.Context, dimension int) error {
	i.dimensions = append(i.dimensions, dimension)
	return nil
}

type testPublisher struct{ attempts []int }

func (p *testPublisher) EnqueueEmbeddingMigration(_ context.Context, _, _ string, attempt int) error {
	p.attempts = append(p.attempts, attempt)
	return nil
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "migration.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&models.KnowledgeBase{}, &models.Chunk{}, &models.ChunkEmbedding{},
		&models.EmbeddingMigration{}, &models.AuditLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func seedMigrationFixture(t *testing.T, db *gorm.DB, chunkCount int) {
	t.Helper()
	now := time.Now().UTC()
	kb := models.KnowledgeBase{
		ID: "kb-1", TenantID: "tenant-1", OwnerID: "owner", Name: "KB", ACLVersion: 1,
		RetrievalConfig: "{}", IndexingStrategy: "{}",
		EmbeddingModel: "source-embed", EmbeddingDimension: 4, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&kb).Error; err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	for i := 0; i < chunkCount; i++ {
		chunk := models.Chunk{
			ID: "chunk-" + string(rune('a'+i)), TenantID: "tenant-1", KnowledgeBaseID: "kb-1",
			DocumentVersionID: "version-1", ParentID: "parent-1", Ordinal: i, Content: "text",
			ContentHash: "hash", Embedding: "vec", EmbeddingDimension: 4,
			Status: "EVIDENCE_READY", CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&chunk).Error; err != nil {
			t.Fatalf("create chunk: %v", err)
		}
	}
}

func newTestService(t *testing.T, db *gorm.DB, allowed bool) (*Service, *testIndexer, *testPublisher, *dbWriter) {
	t.Helper()
	indexer := &testIndexer{}
	publisher := &testPublisher{}
	writer := &dbWriter{db: db}
	service, err := NewService(Options{
		DB: db, Authz: allowAuthz{allowed: allowed},
		Models:          fixedCatalog{binding: embedding.Binding{Model: "target-embed", Dimension: 8}},
		EmbedderFactory: testFactory{embedder: testEmbedder{name: "target-embed", dimension: 8}},
		Writer:          writer, Indexer: indexer, Publisher: publisher, BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service, indexer, publisher, writer
}

func TestMigrationBuildsVerifiesAndCutsOver(t *testing.T) {
	db := openTestDB(t)
	seedMigrationFixture(t, db, 2)
	service, indexer, publisher, writer := newTestService(t, db, true)
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}
	ctx := context.Background()

	job, err := service.Start(ctx, principal, "kb-1", "target-embed")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if job.Status != StatusPending || job.TotalChunks != 2 {
		t.Fatalf("unexpected job: %+v", job)
	}
	if len(publisher.attempts) != 1 {
		t.Fatalf("start must enqueue the first step: %v", publisher.attempts)
	}

	for i := 0; i < 6; i++ {
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		job, err = service.load(ctx, job.ID)
		if err != nil {
			t.Fatalf("load job: %v", err)
		}
		if job.Status == StatusReady {
			break
		}
	}
	if job.Status != StatusReady {
		t.Fatalf("expected ready, got %s", job.Status)
	}
	if len(indexer.dimensions) != 1 || indexer.dimensions[0] != 8 {
		t.Fatalf("target index was not ensured: %v", indexer.dimensions)
	}
	if writer.rows != 2 {
		t.Fatalf("expected two variant rows, got %d", writer.rows)
	}

	cut, err := service.Cutover(ctx, principal, "kb-1", job.ID)
	if err != nil {
		t.Fatalf("cutover: %v", err)
	}
	if cut.Status != StatusCutover {
		t.Fatalf("unexpected cutover status: %s", cut.Status)
	}
	var kb models.KnowledgeBase
	if err := db.Where("id = ?", "kb-1").First(&kb).Error; err != nil {
		t.Fatalf("load knowledge base: %v", err)
	}
	if kb.EmbeddingModel != "target-embed" || kb.EmbeddingDimension != 8 {
		t.Fatalf("binding did not switch: %s/%d", kb.EmbeddingModel, kb.EmbeddingDimension)
	}

	rolled, err := service.Rollback(ctx, principal, "kb-1", job.ID)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rolled.Status != StatusRolledBack {
		t.Fatalf("unexpected rollback status: %s", rolled.Status)
	}
	if err := db.Where("id = ?", "kb-1").First(&kb).Error; err != nil {
		t.Fatalf("reload knowledge base: %v", err)
	}
	if kb.EmbeddingModel != "source-embed" || kb.EmbeddingDimension != 4 {
		t.Fatalf("rollback did not restore the binding: %s/%d", kb.EmbeddingModel, kb.EmbeddingDimension)
	}
}

func TestCancelRemovesTargetAndKeepsSource(t *testing.T) {
	db := openTestDB(t)
	seedMigrationFixture(t, db, 1)
	service, _, _, writer := newTestService(t, db, true)
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}
	ctx := context.Background()
	job, err := service.Start(ctx, principal, "kb-1", "target-embed")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := service.Step(ctx, job.ID); err != nil { // build index -> reembedding
		t.Fatalf("step pending: %v", err)
	}
	if err := service.Step(ctx, job.ID); err != nil { // write target variant
		t.Fatalf("step reembedding: %v", err)
	}
	if writer.rows != 1 {
		t.Fatalf("expected one target row, got %d", writer.rows)
	}
	cancelled, err := service.Cancel(ctx, principal, "kb-1", job.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("unexpected status: %s", cancelled.Status)
	}
	var count int64
	if err := db.Model(&models.ChunkEmbedding{}).Where("model = ?", "target-embed").Count(&count).Error; err != nil {
		t.Fatalf("count target rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("cancel must remove target rows, found %d", count)
	}
	var kb models.KnowledgeBase
	if err := db.Where("id = ?", "kb-1").First(&kb).Error; err != nil {
		t.Fatalf("load knowledge base: %v", err)
	}
	if kb.EmbeddingModel != "source-embed" {
		t.Fatalf("cancel must not change the binding, got %s", kb.EmbeddingModel)
	}
}

func TestWritePermissionIsRequired(t *testing.T) {
	db := openTestDB(t)
	seedMigrationFixture(t, db, 0)
	service, _, _, _ := newTestService(t, db, false)
	if _, err := service.Start(context.Background(), authz.Principal{SubjectID: "viewer", TenantID: "tenant-1"}, "kb-1", "target-embed"); err == nil {
		t.Fatal("expected forbidden")
	}
}
