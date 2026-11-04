package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// activeChildChunkStatuses are the child-chunk states that belong to the
// searchable set. PARENT chunks carry no vector, and STALE chunks are on their
// way out, so neither is expected to hold a current embedding.
var activeChildChunkStatuses = []string{StatusEmbedded, StatusEvidenceReady}

// RebuildResult reports what a rebuild pass touched. Scanned counts every
// active child chunk seen; Reembedded counts the ones that received a new
// vector; Versions counts document versions whose evidence package was
// refreshed.
type RebuildResult struct {
	Scanned    int64
	Reembedded int64
	Versions   int
}

type chunkToEmbed struct {
	ID                string
	DocumentVersionID string
	Content           string
}

// RebuildEmbeddings re-embeds every active child chunk whose stored vector does
// not match the configured model and dimension. It is the data half of the
// managed dimension switch: the matching halfvec(N) index is created first, then
// this runs, then VerifyEmbeddings confirms no old-dimension chunks remain.
//
// Chunk status is preserved so a rebuilt chunk stays part of the same published
// set; the affected evidence packages are refreshed in place so their
// embedding_dimension metadata matches the new vectors.
func RebuildEmbeddings(ctx context.Context, db *gorm.DB, writer EvidenceIndexWriter, embedder Embedder, batchSize int) (RebuildResult, error) {
	if embedder == nil {
		return RebuildResult{}, fmt.Errorf("embedder is required")
	}
	if writer == nil {
		return RebuildResult{}, fmt.Errorf("evidence index writer is required")
	}
	if batchSize <= 0 {
		batchSize = 32
	}
	dimension := embedder.Dimension()
	model := embedder.Name()
	result := RebuildResult{}
	if err := db.WithContext(ctx).Table("chunks").
		Where("parent_id IS NOT NULL AND status IN ?", activeChildChunkStatuses).
		Count(&result.Scanned).Error; err != nil {
		return result, fmt.Errorf("count active chunks: %w", err)
	}
	affected := make(map[string]struct{})
	for {
		// Each pass re-queries the mismatched set instead of tracking an offset:
		// updated rows drop out of the predicate, so the next pass sees the rest.
		var chunks []chunkToEmbed
		if err := db.WithContext(ctx).Table("chunks").
			Select("id, document_version_id, content").
			Where("parent_id IS NOT NULL AND status IN ? AND (embedding_dimension <> ? OR embedding_model <> ?)", activeChildChunkStatuses, dimension, model).
			Order("id").Limit(batchSize).Scan(&chunks).Error; err != nil {
			return result, fmt.Errorf("load chunks needing re-embedding: %w", err)
		}
		if len(chunks) == 0 {
			break
		}
		texts := make([]string, len(chunks))
		for i := range chunks {
			texts[i] = chunks[i].Content
		}
		vectors, err := embedder.Embed(ctx, texts)
		if err != nil {
			return result, fmt.Errorf("embedding_error: %w", err)
		}
		if len(vectors) != len(chunks) {
			return result, fmt.Errorf("embedding_error: vector count mismatch")
		}
		records := make([]EmbeddingRecord, len(chunks))
		for i := range chunks {
			if len(vectors[i]) != dimension {
				return result, fmt.Errorf("embedding_error: vector dimension mismatch for chunk %s: got %d, want %d", chunks[i].ID, len(vectors[i]), dimension)
			}
			records[i] = EmbeddingRecord{ChunkID: chunks[i].ID, Vector: vectors[i]}
			affected[chunks[i].DocumentVersionID] = struct{}{}
		}
		// An empty version id tells the writer to update vectors without changing
		// the published status of each version.
		if err := writer.WriteEmbeddings(ctx, "", model, records); err != nil {
			return result, err
		}
		result.Reembedded += int64(len(chunks))
	}
	if err := refreshEvidencePackages(ctx, db, affected); err != nil {
		return result, err
	}
	// The rebuild is the managed switch for this single-model deployment, so it
	// moves every knowledge base's binding with the vectors. Leaving the
	// bindings behind would make retrieval and ingestion reject the new model.
	if err := db.WithContext(ctx).Model(&models.KnowledgeBase{}).
		Where("embedding_model <> ? OR embedding_dimension <> ?", model, dimension).
		Updates(map[string]any{"embedding_model": model, "embedding_dimension": dimension, "updated_at": time.Now()}).Error; err != nil {
		return result, fmt.Errorf("rebind knowledge bases: %w", err)
	}
	result.Versions = len(affected)
	return result, nil
}

// VerifyEmbeddings counts active child chunks that still carry a vector from a
// different model or dimension. A non-zero result means retrieval would mix
// incomparable vectors and the switch must not be completed.
func VerifyEmbeddings(ctx context.Context, db *gorm.DB, model string, dimension int) (int64, error) {
	if dimension <= 0 {
		return 0, fmt.Errorf("vector dimension must be a positive integer, got %d", dimension)
	}
	var leftover int64
	if err := db.WithContext(ctx).Table("chunks").
		Where("parent_id IS NOT NULL AND status IN ? AND (embedding_dimension <> ? OR embedding_model <> ?)", activeChildChunkStatuses, dimension, model).
		Count(&leftover).Error; err != nil {
		return 0, fmt.Errorf("count chunks with stale embeddings: %w", err)
	}
	return leftover, nil
}

func refreshEvidencePackages(ctx context.Context, db *gorm.DB, versions map[string]struct{}) error {
	if len(versions) == 0 {
		return nil
	}
	versionIDs := make([]string, 0, len(versions))
	for id := range versions {
		versionIDs = append(versionIDs, id)
	}
	var packages []models.EvidencePackage
	if err := db.WithContext(ctx).Where("document_version_id IN ? AND status = ?", versionIDs, StatusEvidenceReady).Find(&packages).Error; err != nil {
		return fmt.Errorf("load evidence packages to refresh: %w", err)
	}
	for _, pkg := range packages {
		var value EvidencePackage
		if err := json.Unmarshal([]byte(pkg.PackageJSON), &value); err != nil {
			return fmt.Errorf("decode evidence package %s: %w", pkg.ID, err)
		}
		chunks, err := loadPackageChunks(ctx, db, pkg.DocumentVersionID)
		if err != nil {
			return err
		}
		value.Chunks = chunks
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal evidence package %s: %w", pkg.ID, err)
		}
		if err := db.WithContext(ctx).Model(&models.EvidencePackage{}).Where("id = ?", pkg.ID).
			Updates(map[string]any{"package_json": string(data), "updated_at": time.Now()}).Error; err != nil {
			return fmt.Errorf("refresh evidence package %s: %w", pkg.ID, err)
		}
	}
	return nil
}

func loadPackageChunks(ctx context.Context, db *gorm.DB, documentVersionID string) ([]Chunk, error) {
	var rows []models.Chunk
	if err := db.WithContext(ctx).
		Where("document_version_id = ? AND status IN ?", documentVersionID, []string{StatusParent, StatusEmbedded, StatusEvidenceReady}).
		Order("ordinal, id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load package chunks: %w", err)
	}
	return projectChunks(rows), nil
}

// projectChunks maps stored chunk rows to the package projection. It is shared
// with the pipeline index stage so a rebuilt package matches what ingestion
// would have published.
func projectChunks(rows []models.Chunk) []Chunk {
	result := make([]Chunk, 0, len(rows))
	for _, item := range rows {
		result = append(result, Chunk{ID: item.ID, ParentID: item.ParentID, Ordinal: item.Ordinal, Content: item.Content, ContentHash: item.ContentHash, HeadingPath: item.HeadingPath, PageNumber: item.PageNumber, SourcePath: item.SourcePath, StartOffset: item.StartOffset, EndOffset: item.EndOffset, EmbeddingDimension: item.EmbeddingDimension})
	}
	return result
}
