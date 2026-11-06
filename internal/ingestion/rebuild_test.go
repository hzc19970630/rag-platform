package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type rebuildEmbedder struct {
	dimension int
	model     string
}

func (e rebuildEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = make([]float32, e.dimension)
		vectors[i][0] = float32(i + 1)
	}
	return vectors, nil
}

func (e rebuildEmbedder) Dimension() int { return e.dimension }
func (e rebuildEmbedder) Name() string   { return e.model }

type rebuildWriter struct {
	db        *gorm.DB
	failAfter int
	written   int
}

func (w *rebuildWriter) IndexChunks(context.Context, string, []ChunkDocument) error { return nil }

func (w *rebuildWriter) IndexVersion(context.Context, string) error { return nil }

func (w *rebuildWriter) DeleteVersion(context.Context, string) error { return nil }

func (w *rebuildWriter) WriteEmbeddings(_ context.Context, _ string, model string, records []EmbeddingRecord) error {
	for _, record := range records {
		if w.failAfter > 0 && w.written >= w.failAfter {
			return errors.New("interrupted")
		}
		if err := w.db.Model(&models.Chunk{}).Where("id = ?", record.ChunkID).
			Updates(map[string]any{"embedding_dimension": len(record.Vector), "embedding_model": model}).Error; err != nil {
			return err
		}
		w.written++
	}
	return nil
}

func openRebuildDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Chunk{}, &models.KnowledgeBase{}, &models.EvidencePackage{}); err != nil {
		t.Fatalf("migrate rebuild tables: %v", err)
	}
	if err := db.Exec("ALTER TABLE chunks ADD COLUMN embedding_model varchar(128) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatalf("add embedding_model: %v", err)
	}
	return db
}

func seedRebuildKB(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&models.KnowledgeBase{ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner", Name: "A", EmbeddingModel: "old", EmbeddingDimension: 3}).Error; err != nil {
		t.Fatalf("seed KB: %v", err)
	}
	for _, id := range []string{"chunk-1", "chunk-2"} {
		if err := db.Exec(`INSERT INTO chunks
			(id, tenant_id, knowledge_base_id, document_version_id, parent_id, ordinal, content, content_hash, status, embedding_dimension, embedding_model, created_at, updated_at)
			VALUES (?, 'tenant-a', 'kb-a', 'version-a', 'parent-a', 1, 'text', 'hash', 'EMBEDDED', 3, 'old', ?, ?)`,
			id, now, now).Error; err != nil {
			t.Fatalf("seed chunk %s: %v", id, err)
		}
	}
}

func TestRebuildEmbeddingsKeepsBindingUntilAllChunksMove(t *testing.T) {
	db := openRebuildDB(t)
	seedRebuildKB(t, db)
	embedder := rebuildEmbedder{dimension: 4, model: "new"}
	writer := &rebuildWriter{db: db, failAfter: 1}
	ctx := context.Background()

	if _, err := RebuildEmbeddings(ctx, db, writer, embedder, 1); err == nil {
		t.Fatal("expected the interrupted writer to fail")
	}
	var kb models.KnowledgeBase
	if err := db.First(&kb, "id = ?", "kb-a").Error; err != nil {
		t.Fatalf("load KB: %v", err)
	}
	if kb.EmbeddingDimension != 3 || kb.EmbeddingModel != "old" {
		t.Fatalf("KB switched before all chunks moved: %+v", kb)
	}

	writer = &rebuildWriter{db: db}
	result, err := RebuildEmbeddings(ctx, db, writer, embedder, 1)
	if err != nil {
		t.Fatalf("resume rebuild: %v", err)
	}
	if result.Reembedded != 1 {
		t.Fatalf("resumed rebuild rewrote %d chunks, want the one remaining", result.Reembedded)
	}
	if err := db.First(&kb, "id = ?", "kb-a").Error; err != nil {
		t.Fatalf("reload KB: %v", err)
	}
	if kb.EmbeddingDimension != 4 || kb.EmbeddingModel != "new" {
		t.Fatalf("KB binding was not switched: %+v", kb)
	}
	var stale int64
	if err := db.Model(&models.Chunk{}).Where("embedding_dimension <> ? OR embedding_model <> ?", 4, "new").Count(&stale).Error; err != nil {
		t.Fatalf("count stale chunks: %v", err)
	}
	if stale != 0 {
		t.Fatalf("%d chunks still on the old vector", stale)
	}
}

type wrongWidthEmbedder struct {
	dimension int
}

func (e wrongWidthEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = make([]float32, 5)
	}
	return vectors, nil
}

func (e wrongWidthEmbedder) Dimension() int { return e.dimension }
func (e wrongWidthEmbedder) Name() string   { return "new" }

func TestRebuildEmbeddingsRejectsWrongVectorDimension(t *testing.T) {
	db := openRebuildDB(t)
	seedRebuildKB(t, db)
	// The embedder declares dimension 4 but returns width 5; the rebuild must
	// refuse before writing a mixed vector into the KB.
	bad := wrongWidthEmbedder{dimension: 4}
	writer := &rebuildWriter{db: db}
	_, err := RebuildEmbeddings(context.Background(), db, writer, bad, 1)
	if err == nil || !strings.Contains(err.Error(), "vector dimension mismatch") {
		t.Fatalf("wrong dimension error = %v", err)
	}
}
