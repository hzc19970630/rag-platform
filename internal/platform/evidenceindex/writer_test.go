package evidenceindex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openIndexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "index.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Chunk{}, &models.DocumentVersion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestWriterValidatesInputBeforeTouchingTheDatabase(t *testing.T) {
	writer := New(openIndexDB(t))
	ctx := context.Background()
	if err := writer.WriteEmbeddings(ctx, "version-1", "model", nil); err == nil || !strings.Contains(err.Error(), "no records") {
		t.Fatalf("empty embedding batch = %v", err)
	}
	if err := writer.WriteEmbeddings(ctx, "version-1", "model", []ports.EmbeddingRecord{{ChunkID: "chunk-1"}}); err == nil || !strings.Contains(err.Error(), "empty embedding") {
		t.Fatalf("empty vector = %v", err)
	}
	if err := writer.WriteVariants(ctx, "model", nil); err != nil {
		t.Fatalf("empty variant batch must be a no-op: %v", err)
	}
	if err := writer.WriteVariants(ctx, "model", []VariantRecord{{ChunkID: "chunk-1", Dimension: 3}}); err == nil || !strings.Contains(err.Error(), "invalid variant vector") {
		t.Fatalf("invalid variant vector = %v", err)
	}
}

func TestWriterReportsMissingChunksAndNoOps(t *testing.T) {
	writer := New(openIndexDB(t))
	ctx := context.Background()
	if err := writer.IndexVersion(ctx, "missing-version"); err == nil || !strings.Contains(err.Error(), "no embedded chunks") {
		t.Fatalf("empty version = %v", err)
	}
	if err := writer.IndexChunks(ctx, "version-1", nil); err != nil {
		t.Fatalf("IndexChunks must be a no-op: %v", err)
	}
	if err := writer.DeleteVersion(ctx, "version-1"); err != nil {
		t.Fatalf("DeleteVersion must be a no-op: %v", err)
	}
	// SQLite cannot execute the halfvec cast; the adapter must surface the
	// database error instead of silently dropping the write.
	err := writer.WriteEmbeddings(ctx, "version-1", "model", []ports.EmbeddingRecord{{ChunkID: "chunk-1", Vector: []float32{1, 2, 3}}})
	if err == nil {
		t.Fatal("halfvec write on SQLite must fail")
	}
}
