package app

import (
	"context"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/vectorindex"
	"github.com/hzc19970630/rag-platform/internal/embeddingmigration"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
)

// embeddingIndexEnsurer bridges the migration domain to the pgvector index
// manager. Keeping it here avoids a domain -> provider-adapter import.
type embeddingIndexEnsurer struct {
	manager          vectorindex.Manager
	lockTimeout      time.Duration
	statementTimeout time.Duration
}

func (e embeddingIndexEnsurer) EnsureIndex(ctx context.Context, dimension int) error {
	_, err := e.manager.EnsureChunkEmbeddingIndex(ctx, dimension, vectorindex.Options{
		LockTimeout:      e.lockTimeout,
		StatementTimeout: e.statementTimeout,
	})
	return err
}

type variantSink interface {
	WriteVariants(ctx context.Context, model string, records []evidenceindex.VariantRecord) error
}

type variantWriter struct {
	writer variantSink
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
