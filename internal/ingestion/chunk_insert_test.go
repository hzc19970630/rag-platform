package ingestion

import (
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func TestInsertChunksWritesEveryRowAcrossBatches(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&models.Chunk{}); err != nil {
		t.Fatalf("migrate chunks: %v", err)
	}
	previousBatchSize := chunkInsertBatchSize
	chunkInsertBatchSize = 2
	defer func() { chunkInsertBatchSize = previousBatchSize }()

	version := models.DocumentVersion{ID: "ver-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-1"}
	rows := []chunkRow{
		{ID: "parent-1", Content: "section one", ContentHash: "h1", HeadingPath: "H1", PageNumber: 1, SourcePath: "a.md", StartOffset: 0, EndOffset: 10, Ordinal: 0},
		{ID: "chunk-1", ParentID: "parent-1", Content: "child one", ContentHash: "h2", HeadingPath: "H1", PageNumber: 1, SourcePath: "a.md", StartOffset: 0, EndOffset: 9, Ordinal: 0},
		{ID: "chunk-2", ParentID: "parent-1", Content: "child two", ContentHash: "h3", HeadingPath: "H1", PageNumber: 1, SourcePath: "a.md", StartOffset: 10, EndOffset: 18, Ordinal: 1},
		{ID: "parent-2", Content: "section two", ContentHash: "h4", HeadingPath: "H2", PageNumber: 2, SourcePath: "a.md", StartOffset: 20, EndOffset: 30, Ordinal: 1},
		{ID: "chunk-3", ParentID: "parent-2", Content: "child three", ContentHash: "h5", HeadingPath: "H2", PageNumber: 2, SourcePath: "a.md", StartOffset: 20, EndOffset: 28, Ordinal: 2},
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return insertChunks(tx, version, rows, now)
	}); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}

	var stored []models.Chunk
	if err := db.Order("id").Find(&stored).Error; err != nil {
		t.Fatalf("load chunks: %v", err)
	}
	if len(stored) != len(rows) {
		t.Fatalf("stored %d chunks, want %d", len(stored), len(rows))
	}

	var parentCount int64
	if err := db.Model(&models.Chunk{}).Where("parent_id IS NULL").Count(&parentCount).Error; err != nil {
		t.Fatalf("count parents: %v", err)
	}
	if parentCount != 2 {
		t.Fatalf("parent rows = %d, want 2 (empty parent_id must be NULL)", parentCount)
	}

	for _, chunk := range stored {
		if chunk.CreatedAt.IsZero() || chunk.UpdatedAt.IsZero() {
			t.Fatalf("chunk %s has zero timestamps: %+v", chunk.ID, chunk)
		}
		if chunk.ParentID == "" && chunk.Status != StatusParent {
			t.Fatalf("parent chunk %s status = %q", chunk.ID, chunk.Status)
		}
		if chunk.ParentID != "" && chunk.Status != StatusChunked {
			t.Fatalf("child chunk %s status = %q", chunk.ID, chunk.Status)
		}
		if chunk.EmbeddingDimension != 0 {
			t.Fatalf("chunk %s embedding dimension = %d, want 0", chunk.ID, chunk.EmbeddingDimension)
		}
	}
}
