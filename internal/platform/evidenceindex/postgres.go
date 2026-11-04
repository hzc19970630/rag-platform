// Package evidenceindex is the PostgreSQL implementation of the Evidence Index
// write port. Vectors live on the chunk row and BM25 is maintained by
// PostgreSQL, so the adapter persists embeddings and verifies the projection;
// a search-engine adapter can replace it without touching the pipeline.
package evidenceindex

import (
	"context"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/vecutil"
	"gorm.io/gorm"
)

type Writer struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Writer { return &Writer{db: db} }

var _ ingestion.EvidenceIndexWriter = (*Writer)(nil)

func (w *Writer) WriteEmbeddings(ctx context.Context, versionID, model string, records []ingestion.EmbeddingRecord) error {
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
				args = append(args, ingestion.StatusEmbedded)
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
		}
		if versionID == "" {
			return nil
		}
		return tx.Model(&models.DocumentVersion{}).Where("id = ?", versionID).Updates(map[string]any{
			"status": ingestion.StatusEmbedded, "error_code": "", "error_message": "", "updated_at": now,
		}).Error
	})
}

func (w *Writer) IndexVersion(ctx context.Context, versionID string) error {
	var count int64
	if err := w.db.WithContext(ctx).Table("chunks").
		Where("document_version_id = ? AND parent_id IS NOT NULL AND status = ?", versionID, ingestion.StatusEmbedded).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no embedded chunks for document version %s", versionID)
	}
	return nil
}

// DeleteVersion is a no-op for PostgreSQL: chunks and their BM25 rows are
// deleted by the pipeline itself. External index adapters must implement it.
func (w *Writer) DeleteVersion(context.Context, string) error { return nil }
