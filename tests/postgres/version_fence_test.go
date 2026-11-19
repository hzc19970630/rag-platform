package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

// A version cancelled by a replacement must never receive embeddings or flip
// back to EMBEDDED, even if the stage started before the supersede committed.
func TestWriteEmbeddingsRefusesCancelledVersion(t *testing.T) {
	db := openGuardDB(t)
	ctx := context.Background()
	now := time.Now()
	suffix := fmt.Sprintf("%d", now.UnixNano())
	tenantID := "ten-fence-" + suffix
	userID := "user-fence-" + suffix
	kbID := "kb-fence-" + suffix
	documentID := "doc-fence-" + suffix
	versionID := "ver-fence-" + suffix
	chunkID := "chunk-fence-" + suffix

	t.Cleanup(func() {
		db.Where("tenant_id = ?", tenantID).Delete(&models.Chunk{})
		db.Where("tenant_id = ?", tenantID).Delete(&models.DocumentVersion{})
		db.Where("tenant_id = ?", tenantID).Delete(&models.Document{})
		db.Where("tenant_id = ?", tenantID).Delete(&models.KnowledgeBase{})
		db.Where("id = ?", userID).Delete(&models.User{})
		db.Where("id = ?", tenantID).Delete(&models.Tenant{})
	})

	for _, seed := range []any{
		&models.User{ID: userID, Email: userID + "@example.test", DisplayName: "Fence User", CreatedAt: now, UpdatedAt: now},
		&models.Tenant{ID: tenantID, Name: tenantID, Quota: "{}", RetrievalConfig: "{}", ChunkingConfig: "{}", CreatedAt: now, UpdatedAt: now},
		&models.KnowledgeBase{ID: kbID, TenantID: tenantID, OwnerID: userID, Name: "Fence KB", ACLVersion: 1, CreatedAt: now, UpdatedAt: now},
		&models.Document{ID: documentID, TenantID: tenantID, KnowledgeBaseID: kbID, OwnerID: userID, Filename: "fence.txt", MimeType: "text/plain", Status: "PARSING", LatestVersionID: versionID, CreatedAt: now, UpdatedAt: now},
		&models.DocumentVersion{ID: versionID, DocumentID: documentID, TenantID: tenantID, KnowledgeBaseID: kbID, Version: 1, ContentHash: "hash-" + suffix, SizeBytes: 5, SourceObjectKey: "source-" + suffix, Status: "CANCELLED", ACLVersion: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(seed).Error; err != nil {
			t.Fatalf("seed %T: %v", seed, err)
		}
	}
	// The halfvec column must stay NULL, so the zero-value string is omitted.
	seedChunk := models.Chunk{ID: chunkID, TenantID: tenantID, KnowledgeBaseID: kbID, DocumentVersionID: versionID, Ordinal: 0, Content: "hello", ContentHash: "chunk-" + suffix, Status: "CHUNKED", CreatedAt: now, UpdatedAt: now}
	if err := db.Omit("Embedding").Create(&seedChunk).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	writer := evidenceindex.New(db)
	err := writer.WriteEmbeddings(ctx, versionID, "test-model", []ports.EmbeddingRecord{
		{ChunkID: chunkID, Vector: []float32{0.1, 0.2, 0.3, 0.4}},
	})
	if !errors.Is(err, apperror.ErrVersionSuperseded) {
		t.Fatalf("WriteEmbeddings = %v, want ErrVersionSuperseded", err)
	}

	var chunk models.Chunk
	if err := db.First(&chunk, "id = ?", chunkID).Error; err != nil {
		t.Fatalf("load chunk: %v", err)
	}
	if chunk.EmbeddingDimension != 0 || chunk.Status != "CHUNKED" {
		t.Fatalf("cancelled version was written: dimension=%d status=%q", chunk.EmbeddingDimension, chunk.Status)
	}
	var version models.DocumentVersion
	if err := db.First(&version, "id = ?", versionID).Error; err != nil {
		t.Fatalf("load version: %v", err)
	}
	if version.Status != "CANCELLED" {
		t.Fatalf("version status = %q, want CANCELLED", version.Status)
	}
}
