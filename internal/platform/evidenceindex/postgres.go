// Package evidenceindex is the PostgreSQL implementation of the Evidence Index
// write port. Vectors live on the chunk row and BM25 is maintained by
// PostgreSQL, so the adapter persists embeddings and verifies the projection;
// a search-engine adapter can replace it without touching the pipeline.
package evidenceindex

import (
	"context"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/vecutil"
	"gorm.io/gorm"
)

type Writer struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Writer { return &Writer{db: db} }

var _ ports.EvidenceIndexWriter = (*Writer)(nil)

func (w *Writer) WriteEmbeddings(ctx context.Context, versionID, model string, records []ports.EmbeddingRecord) error {
	if len(records) == 0 {
		return fmt.Errorf("embedding writer received no records")
	}
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for _, record := range records {
			if len(record.Vector) == 0 {
				return fmt.Errorf("chunk %s has an empty embedding", record.ChunkID)
			}
			query := "UPDATE chunks SET embedding = ?::halfvec, embedding_dimension = ?, embedding_model = ?, updated_at = ?"
			args := []any{vecutil.Literal(record.Vector), len(record.Vector), model, now}
			if versionID != "" {
				query += ", status = ?"
				args = append(args, ports.StatusEmbedded)
			}
			query += " WHERE id = ?"
			args = append(args, record.ChunkID)
			if versionID != "" {
				query += " AND document_version_id = ?"
				args = append(args, versionID)
			}
			result := tx.Exec(query, args...)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("chunk %s is not part of version %s", record.ChunkID, versionID)
			}
			// Dual-write the variant row during the transition. Search reads the
			// variant table; the legacy column keeps a rolling deployment and
			// the migration rollback path readable.
			if err := tx.Exec(
				`INSERT INTO chunk_embeddings (chunk_id, tenant_id, knowledge_base_id, model, dimension, embedding, created_at, updated_at)
				 SELECT id, tenant_id, knowledge_base_id, ?, ?, ?::halfvec, ?, ? FROM chunks WHERE id = ?
				 ON CONFLICT (chunk_id, model) DO UPDATE SET
				   tenant_id = EXCLUDED.tenant_id,
				   knowledge_base_id = EXCLUDED.knowledge_base_id,
				   dimension = EXCLUDED.dimension,
				   embedding = EXCLUDED.embedding,
				   updated_at = EXCLUDED.updated_at`,
				model, len(record.Vector), vecutil.Literal(record.Vector), now, now, record.ChunkID,
			).Error; err != nil {
				return fmt.Errorf("store embedding variant for chunk %s: %w", record.ChunkID, err)
			}
		}
		if versionID == "" {
			return nil
		}
		return tx.Model(&models.DocumentVersion{}).Where("id = ?", versionID).Updates(map[string]any{
			"status": ports.StatusEmbedded, "error_code": "", "error_message": "", "updated_at": now,
		}).Error
	})
}

func (w *Writer) IndexVersion(ctx context.Context, versionID string) error {
	var count int64
	if err := w.db.WithContext(ctx).Table("chunks").
		Where("document_version_id = ? AND parent_id IS NOT NULL AND status = ?", versionID, ports.StatusEmbedded).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no embedded chunks for document version %s", versionID)
	}
	return nil
}

// IndexChunks is a no-op for PostgreSQL: BM25 is maintained by the chunk row
// itself. An external keyword/hybrid adapter receives the same documents here.
func (w *Writer) IndexChunks(context.Context, string, []ports.ChunkDocument) error {
	return nil
}

// DeleteVersion is a no-op for PostgreSQL: chunks and their BM25 rows are
// deleted by the pipeline itself. External index adapters must implement it.
func (w *Writer) DeleteVersion(context.Context, string) error { return nil }

// VariantRecord is one target-space vector written by a managed migration. The
// chunk row already exists; only the variant row is new.
type VariantRecord struct {
	ChunkID         string
	TenantID        string
	KnowledgeBaseID string
	Dimension       int
	Vector          []float32
}

// WriteVariants upserts target-space vectors without touching the legacy chunk
// columns, so the source space stays searchable until cutover.
func (w *Writer) WriteVariants(ctx context.Context, model string, records []VariantRecord) error {
	if len(records) == 0 {
		return nil
	}
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for _, record := range records {
			if len(record.Vector) == 0 || len(record.Vector) != record.Dimension {
				return fmt.Errorf("chunk %s has an invalid variant vector", record.ChunkID)
			}
			if err := tx.Exec(
				`INSERT INTO chunk_embeddings (chunk_id, tenant_id, knowledge_base_id, model, dimension, embedding, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?::halfvec, ?, ?)
				 ON CONFLICT (chunk_id, model) DO UPDATE SET
				   tenant_id = EXCLUDED.tenant_id,
				   knowledge_base_id = EXCLUDED.knowledge_base_id,
				   dimension = EXCLUDED.dimension,
				   embedding = EXCLUDED.embedding,
				   updated_at = EXCLUDED.updated_at`,
				record.ChunkID, record.TenantID, record.KnowledgeBaseID, model, record.Dimension,
				vecutil.Literal(record.Vector), now, now,
			).Error; err != nil {
				return fmt.Errorf("write embedding variant for chunk %s: %w", record.ChunkID, err)
			}
		}
		return nil
	})
}
