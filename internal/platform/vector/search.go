package vector

import (
	"context"
	"fmt"

	"github.com/example/rag-platform/internal/platform/vecutil"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
)

// Searcher runs cosine recall against the chunks HNSW index. The index is
// partial on embedding_dimension, so the query filters on the same column and
// the dimension must be inlined as a type modifier.
type Searcher struct {
	db *gorm.DB
}

func NewSearcher(db *gorm.DB) *Searcher { return &Searcher{db: db} }

const vectorRecallTemplate = `
SELECT c.id AS chunk_id,
       dv.document_id AS document_id,
       c.document_version_id AS document_version_id,
       c.knowledge_base_id AS knowledge_base_id,
       COALESCE(c.parent_id, '') AS parent_id,
       c.ordinal AS ordinal,
       c.content AS content,
       c.content_hash AS content_hash,
       c.heading_path AS heading_path,
       c.page_number AS page_number,
       c.source_path AS source_path,
       1 - (c.embedding::halfvec(%d) <=> ?::halfvec(%d)) AS score
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = ?
  AND c.knowledge_base_id IN ?
  AND c.status = ?
  AND c.parent_id IS NOT NULL
  AND c.embedding_dimension = %d
  AND c.embedding_model = ?
  AND c.embedding IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = ?
  AND d.deleted_at IS NULL
ORDER BY c.embedding::halfvec(%d) <=> ?::halfvec(%d) ASC
LIMIT ?`

func (s *Searcher) SearchVector(ctx context.Context, request retrieval.RecallRequest, embedding []float32) ([]retrieval.Hit, error) {
	dimension := len(embedding)
	if dimension <= 0 {
		return nil, fmt.Errorf("query embedding is empty")
	}
	if request.TopK <= 0 || len(request.KnowledgeBaseIDs) == 0 {
		return nil, nil
	}
	// dimension is a validated positive integer, so inlining it as a type
	// modifier is safe; it cannot carry user input.
	query := fmt.Sprintf(vectorRecallTemplate, dimension, dimension, dimension, dimension, dimension)
	literal := vecutil.Literal(embedding)
	rows, err := s.db.WithContext(ctx).Raw(query, literal, request.TenantID, request.KnowledgeBaseIDs, status.EvidenceReady, request.EmbeddingModel, status.EvidenceReady, literal, request.TopK).Rows()
	if err != nil {
		return nil, fmt.Errorf("vector recall: %w", err)
	}
	defer rows.Close()

	var hits []retrieval.Hit
	rank := 0
	for rows.Next() {
		var hit retrieval.Hit
		if err := rows.Scan(&hit.ChunkID, &hit.DocumentID, &hit.DocumentVersionID, &hit.KnowledgeBaseID, &hit.ParentID, &hit.Ordinal, &hit.Content, &hit.ContentHash, &hit.HeadingPath, &hit.PageNumber, &hit.SourcePath, &hit.Score); err != nil {
			return nil, fmt.Errorf("scan vector hit: %w", err)
		}
		// Similarity is 1 - cosine distance, so the threshold is a 0-1 floor.
		if hit.Score < request.Threshold {
			continue
		}
		rank++
		hit.Rank = rank
		hit.Channel = retrieval.ChannelVector
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
