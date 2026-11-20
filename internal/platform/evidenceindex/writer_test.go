package evidenceindex

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
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
	db := openIndexDB(t)
	writer := New(db)
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
	version := createVersion(t, db, "version-live", ports.StatusEmbedded)
	// The version fence passes for a live version; SQLite then cannot execute
	// the halfvec cast, and the adapter must surface that database error.
	err := writer.WriteEmbeddings(ctx, version.ID, "model", []ports.EmbeddingRecord{{ChunkID: "chunk-1", Vector: []float32{1, 2, 3}}})
	if err == nil || errors.Is(err, apperror.ErrVersionSuperseded) {
		t.Fatalf("halfvec write on SQLite must fail after the fence, got %v", err)
	}
}

func TestWriterRejectsSupersededVersionBeforeWriting(t *testing.T) {
	db := openIndexDB(t)
	version := createVersion(t, db, "version-cancelled", ports.StatusCancelled)
	chunk := createChunk(t, db, "chunk-fenced", version.ID)

	writer := New(db)
	err := writer.WriteEmbeddings(context.Background(), version.ID, "model", []ports.EmbeddingRecord{{
		ChunkID: chunk.ID,
		Vector:  []float32{1, 2, 3},
	}})
	if !errors.Is(err, apperror.ErrVersionSuperseded) {
		t.Fatalf("cancelled version error = %v", err)
	}

	var stored models.Chunk
	if err := db.First(&stored, "id = ?", chunk.ID).Error; err != nil {
		t.Fatalf("read chunk: %v", err)
	}
	if stored.EmbeddingDimension != 0 || stored.Status != ports.StatusEmbedded {
		t.Fatalf("cancelled version was written: dimension=%d status=%s", stored.EmbeddingDimension, stored.Status)
	}
}

func TestWriterRejectsUnknownVersion(t *testing.T) {
	writer := New(openIndexDB(t))
	err := writer.WriteEmbeddings(context.Background(), "missing-version", "model", []ports.EmbeddingRecord{{
		ChunkID: "chunk-1",
		Vector:  []float32{1, 2, 3},
	}})
	if !errors.Is(err, apperror.ErrVersionSuperseded) {
		t.Fatalf("missing version error = %v", err)
	}
}

func TestIndexVersionFindsEmbeddedChunks(t *testing.T) {
	db := openIndexDB(t)
	chunk := createChunk(t, db, "chunk-embedded", "version-1")

	writer := New(db)
	if err := writer.IndexVersion(context.Background(), chunk.DocumentVersionID); err != nil {
		t.Fatalf("embedded chunk = %v", err)
	}
}

func TestWriteVariantsValidatesDimensionAndSurfacesStoreErrors(t *testing.T) {
	writer := New(openIndexDB(t))
	ctx := context.Background()

	err := writer.WriteVariants(ctx, "model", []VariantRecord{{
		ChunkID:         "chunk-1",
		TenantID:        "tenant-1",
		KnowledgeBaseID: "kb-1",
		Dimension:       3,
		Vector:          []float32{1, 2},
	}})
	if err == nil || !strings.Contains(err.Error(), "invalid variant vector") {
		t.Fatalf("mismatched dimension = %v", err)
	}

	// SQLite cannot execute the halfvec cast; the valid path must surface the
	// database error rather than dropping the variant write.
	err = writer.WriteVariants(ctx, "model", []VariantRecord{{
		ChunkID:         "chunk-2",
		TenantID:        "tenant-1",
		KnowledgeBaseID: "kb-1",
		Dimension:       3,
		Vector:          []float32{1, 2, 3},
	}})
	if err == nil {
		t.Fatal("halfvec variant write on SQLite must fail")
	}
}

func createVersion(t *testing.T, db *gorm.DB, id, status string) models.DocumentVersion {
	t.Helper()
	now := time.Now()
	version := models.DocumentVersion{
		ID:              id,
		DocumentID:      "doc-1",
		TenantID:        "tenant-1",
		KnowledgeBaseID: "kb-1",
		Version:         1,
		ContentHash:     "hash",
		SizeBytes:       1,
		SourceObjectKey: "source",
		Status:          status,
		ACLVersion:      1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(&version).Error; err != nil {
		t.Fatalf("create version: %v", err)
	}
	return version
}

func createChunk(t *testing.T, db *gorm.DB, id, versionID string) models.Chunk {
	t.Helper()
	now := time.Now()
	chunk := models.Chunk{
		ID:                id,
		TenantID:          "tenant-1",
		KnowledgeBaseID:   "kb-1",
		DocumentVersionID: versionID,
		ParentID:          "parent-1",
		Content:           "content",
		ContentHash:       "hash",
		Status:            ports.StatusEmbedded,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := db.Create(&chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	return chunk
}
