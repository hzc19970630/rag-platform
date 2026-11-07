package vector

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/platform/vecutil"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
)

// maxVectorLimit bounds an accidental oversample configuration. It is a
// safety valve, not a tuning target: the probe never needs more than a few
// hundred rows per query.
const maxVectorLimit = 2000

// Searcher runs cosine recall against the chunks HNSW index. The index is
// partial on embedding_dimension, so the query filters on the same column and
// the dimension must be inlined as a type modifier.
type Searcher struct {
	db      *gorm.DB
	metrics metrics.Recorder
}

// NewSearcherWithMetrics shares the process metrics registry with the vector
// adapter so underfilled tenant-filtered scans are visible in /metrics.
func NewSearcherWithMetrics(db *gorm.DB, recorder metrics.Recorder) *Searcher {
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	return &Searcher{db: db, metrics: recorder}
}

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

const vectorEFSearchStatement = "SET LOCAL hnsw.ef_search = %d"

// vectorIterativeScanStatement enables pgvector's filtered-scan continuation.
// It is a fixed literal rather than a configurable string so a caller cannot
// inject an arbitrary SET value.
const vectorIterativeScanStatement = "SET LOCAL hnsw.iterative_scan = relaxed_order"

// vectorScanLimit bounds the SQL LIMIT used for one recall query. Overfetch is
// only useful up to the point where the filtered result can fill TopK; the cap
// keeps a misconfigured oversample from turning into an accidental full scan.
func vectorScanLimit(topK, oversample int) int {
	if oversample <= 0 {
		oversample = 1
	}
	limit := topK * oversample
	if limit > maxVectorLimit {
		limit = maxVectorLimit
	}
	return limit
}

func (s *Searcher) SearchVector(ctx context.Context, request ports.RecallRequest, embedding []float32) ([]ports.Hit, error) {
	dimension := len(embedding)
	if dimension <= 0 {
		return nil, fmt.Errorf("query embedding is empty")
	}
	if request.TopK <= 0 || len(request.KnowledgeBaseIDs) == 0 {
		return nil, nil
	}
	efSearch := request.EFSearch
	if efSearch < 0 {
		return nil, fmt.Errorf("vector ef_search must not be negative")
	}
	oversample := request.Oversample
	if oversample <= 0 {
		oversample = 1
	}
	limit := vectorScanLimit(request.TopK, oversample)
	// dimension is a validated positive integer, so inlining it as a type
	// modifier is safe; it cannot carry user input.
	query := fmt.Sprintf(vectorRecallTemplate, dimension, dimension, dimension, dimension, dimension)
	literal := vecutil.Literal(embedding)

	var hits []ports.Hit
	err := tenantdb.InTx(ctx, s.db, request.TenantID, func(tx *gorm.DB) error {
		if efSearch > 0 {
			// efSearch is a validated non-negative int, so inlining is safe.
			if err := tx.Exec(fmt.Sprintf(vectorEFSearchStatement, efSearch)).Error; err != nil {
				return fmt.Errorf("set hnsw.ef_search: %w", err)
			}
		}
		if request.IterativeScan {
			if err := tx.Exec(vectorIterativeScanStatement).Error; err != nil {
				return fmt.Errorf("set hnsw.iterative_scan: %w", err)
			}
		}
		rows, err := tx.Raw(query, literal, request.TenantID, request.KnowledgeBaseIDs, status.EvidenceReady, request.EmbeddingModel, status.EvidenceReady, literal, limit).Rows()
		if err != nil {
			return fmt.Errorf("vector recall: %w", err)
		}
		defer rows.Close()

		rank := 0
		for rows.Next() {
			var hit ports.Hit
			if err := rows.Scan(&hit.ChunkID, &hit.DocumentID, &hit.DocumentVersionID, &hit.KnowledgeBaseID, &hit.ParentID, &hit.Ordinal, &hit.Content, &hit.ContentHash, &hit.HeadingPath, &hit.PageNumber, &hit.SourcePath, &hit.Score); err != nil {
				return fmt.Errorf("scan vector hit: %w", err)
			}
			// Similarity is 1 - cosine distance, so the threshold is a 0-1 floor.
			if hit.Score < request.Threshold {
				continue
			}
			rank++
			hit.Rank = rank
			hit.Channel = ports.ChannelVector
			hits = append(hits, hit)
			if len(hits) == request.TopK {
				break
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	// An underfilled scan is the symptom of a tenant filter applied after the
	// ANN candidate list was chosen. Keep the signal visible instead of
	// silently returning fewer rows than the caller asked for.
	if len(hits) < request.TopK {
		if s.metrics != nil {
			s.metrics.Inc("vector_recall_underfill_total", metrics.Labels(
				"ef_search", strconv.Itoa(efSearch),
				"oversample", strconv.Itoa(oversample),
			))
		}
		slog.Warn("vector_recall_underfill",
			"tenant_id", request.TenantID,
			"knowledge_base_count", len(request.KnowledgeBaseIDs),
			"requested", request.TopK,
			"returned", len(hits),
			"ef_search", efSearch,
			"oversample", oversample,
		)
	}
	return hits, nil
}
