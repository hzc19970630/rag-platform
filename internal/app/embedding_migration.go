package app

import (
	"context"
	"time"

	"github.com/hzc19970630/rag-platform/internal/embeddingmigration"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
	"github.com/hzc19970630/rag-platform/internal/platform/vector"
	"gorm.io/gorm"
)

// embeddingIndexEnsurer bridges the migration domain to the pgvector index
// manager. Keeping it here avoids a domain -> provider-adapter import.
type embeddingIndexEnsurer struct {
	db               *gorm.DB
	lockTimeout      time.Duration
	statementTimeout time.Duration
}

func (e embeddingIndexEnsurer) EnsureIndex(ctx context.Context, dimension int) error {
	_, err := vector.EnsureChunkEmbeddingIndex(ctx, e.db, dimension, vector.IndexOptions{
		LockTimeout:      e.lockTimeout,
		StatementTimeout: e.statementTimeout,
	})
	return err
}

type variantWriter struct {
	writer *evidenceindex.Writer
}

func (w variantWriter) WriteVariants(ctx context.Context, model string, records []embeddingmigration.VariantRecord) error {
	converted := make([]evidenceindex.VariantRecord, len(records))
	for i, record := range records {
		converted[i] = evidenceindex.VariantRecord{
			ChunkID: record.ChunkID, TenantID: record.TenantID, KnowledgeBaseID: record.KnowledgeBaseID,
			Dimension: record.Dimension, Vector: record.Vector,
		}
	}
	return w.writer.WriteVariants(ctx, model, converted)
}
