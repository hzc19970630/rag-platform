package search

import (
	"context"
	"fmt"
	"sort"

	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"github.com/hzc19970630/rag-platform/internal/status"
	"gorm.io/gorm"
)

// ChunkExpander loads parent chunks and sibling neighbours around the selected
// evidence. Every extra chunk is a real, citable row: expansion adds context
// without inventing synthetic text. Scope filters are reapplied so expansion
// cannot pull in a chunk from outside the caller's authorization scope.
type ChunkExpander struct {
	db *gorm.DB
}

func NewChunkExpander(db *gorm.DB) *ChunkExpander {
	return &ChunkExpander{db: db}
}

const chunkColumnList = `
       c.id AS chunk_id,
       dv.document_id AS document_id,
       c.document_version_id AS document_version_id,
       c.knowledge_base_id AS knowledge_base_id,
       COALESCE(c.parent_id, '') AS parent_id,
       c.ordinal AS ordinal,
       c.content AS content,
       c.content_hash AS content_hash,
       c.heading_path AS heading_path,
       c.page_number AS page_number,
       c.source_path AS source_path`

const parentChunksQuery = `
SELECT` + chunkColumnList + `
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = ?
  AND c.knowledge_base_id IN ?
  AND c.status IN (?, ?)
  AND c.id IN ?
  AND d.deleted_at IS NULL
  AND d.latest_version_id = dv.id`

const neighbourChunksQuery = `
SELECT` + chunkColumnList + `
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = ?
  AND c.document_version_id = ?
  AND c.status = ?
  AND c.parent_id IS NOT NULL
  AND c.ordinal BETWEEN ? AND ?
  AND d.deleted_at IS NULL
  AND d.latest_version_id = dv.id
ORDER BY c.ordinal`

func (e *ChunkExpander) Expand(ctx context.Context, request ports.RecallRequest, selected []ports.Candidate, options ports.ExpansionOptions) ([]ports.Candidate, error) {
	if len(selected) == 0 {
		return selected, nil
	}
	result := make([]ports.Candidate, len(selected))
	copy(result, selected)
	seen := map[string]bool{}
	anchorScore := map[string]float64{}
	for _, candidate := range selected {
		seen[candidate.ChunkID] = true
		if candidate.Score > anchorScore[candidate.ChunkID] {
			anchorScore[candidate.ChunkID] = candidate.Score
		}
	}

	if err := e.appendParents(ctx, request, selected, &result, seen, anchorScore, options.ScoreFactor); err != nil {
		return nil, err
	}
	if options.PerSide > 0 && options.MaxTotal > 0 {
		if err := e.appendNeighbours(ctx, request, selected, options, &result, seen, anchorScore); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (e *ChunkExpander) appendParents(ctx context.Context, request ports.RecallRequest, selected []ports.Candidate, result *[]ports.Candidate, seen map[string]bool, anchorScore map[string]float64, scoreFactor float64) error {
	parentIDs := make([]string, 0, len(selected))
	for _, candidate := range selected {
		if candidate.ParentID != "" && !seen[candidate.ParentID] {
			parentIDs = append(parentIDs, candidate.ParentID)
		}
	}
	if len(parentIDs) == 0 {
		return nil
	}
	hits, err := e.load(ctx, parentChunksQuery, request.TenantID, request.KnowledgeBaseIDs, status.Parent, status.EvidenceReady, parentIDs)
	if err != nil {
		return err
	}
	for _, hit := range hits {
		if seen[hit.ChunkID] {
			continue
		}
		anchor := bestAnchor(selected, hit.ChunkID, anchorScore)
		appendExpanded(result, seen, hit, ports.ChannelParent, anchor, scoreFactor)
	}
	return nil
}

func (e *ChunkExpander) appendNeighbours(ctx context.Context, request ports.RecallRequest, selected []ports.Candidate, options ports.ExpansionOptions, result *[]ports.Candidate, seen map[string]bool, anchorScore map[string]float64) error {
	ordinalsByVersion := map[string][]int{}
	for _, candidate := range selected {
		if candidate.DocumentVersionID == "" {
			continue
		}
		ordinalsByVersion[candidate.DocumentVersionID] = append(ordinalsByVersion[candidate.DocumentVersionID], candidate.Ordinal)
	}
	// The cap makes iteration order observable: without a stable order the same
	// query could drop different neighbours once MaxTotal is reached.
	versionIDs := make([]string, 0, len(ordinalsByVersion))
	for versionID := range ordinalsByVersion {
		versionIDs = append(versionIDs, versionID)
	}
	sort.Strings(versionIDs)

	added := 0
	for _, versionID := range versionIDs {
		ordinals := ordinalsByVersion[versionID]
		low, high := neighbourWindow(ordinals, options.PerSide)
		hits, err := e.load(ctx, neighbourChunksQuery, request.TenantID, versionID, status.EvidenceReady, low, high)
		if err != nil {
			return err
		}
		for _, hit := range hits {
			if added >= options.MaxTotal {
				return nil
			}
			if seen[hit.ChunkID] || !withinWindow(hit.Ordinal, ordinals, options.PerSide) {
				continue
			}
			anchor := bestAnchor(selected, hit.ChunkID, anchorScore)
			appendExpanded(result, seen, hit, ports.ChannelNeighbor, anchor, options.ScoreFactor)
			added++
		}
	}
	return nil
}

// neighbourWindow is the ordinal span loaded around the selected chunks of one
// document version: perSide neighbours on each side of the outermost anchors.
// Loading the span in one query keeps expansion to a single round trip.
func neighbourWindow(ordinals []int, perSide int) (int, int) {
	low, high := ordinals[0], ordinals[0]
	for _, ordinal := range ordinals {
		if ordinal < low {
			low = ordinal
		}
		if ordinal > high {
			high = ordinal
		}
	}
	return low - perSide, high + perSide
}

func (e *ChunkExpander) load(ctx context.Context, query string, values ...any) ([]ports.Hit, error) {
	rows, err := e.db.WithContext(ctx).Raw(query, values...).Rows()
	if err != nil {
		return nil, fmt.Errorf("load expansion chunks: %w", err)
	}
	defer rows.Close()
	var hits []ports.Hit
	for rows.Next() {
		var hit ports.Hit
		if err := rows.Scan(&hit.ChunkID, &hit.DocumentID, &hit.DocumentVersionID, &hit.KnowledgeBaseID, &hit.ParentID, &hit.Ordinal, &hit.Content, &hit.ContentHash, &hit.HeadingPath, &hit.PageNumber, &hit.SourcePath); err != nil {
			return nil, fmt.Errorf("scan expansion chunk: %w", err)
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func appendExpanded(result *[]ports.Candidate, seen map[string]bool, hit ports.Hit, channel string, anchor float64, scoreFactor float64) {
	seen[hit.ChunkID] = true
	*result = append(*result, ports.Candidate{
		ChunkID:           hit.ChunkID,
		DocumentID:        hit.DocumentID,
		DocumentVersionID: hit.DocumentVersionID,
		KnowledgeBaseID:   hit.KnowledgeBaseID,
		ParentID:          hit.ParentID,
		Ordinal:           hit.Ordinal,
		Content:           hit.Content,
		ContentHash:       hit.ContentHash,
		HeadingPath:       hit.HeadingPath,
		PageNumber:        hit.PageNumber,
		SourcePath:        hit.SourcePath,
		Score:             anchor * scoreFactor,
		Channels:          []string{channel},
	})
}

// bestAnchor scores an expanded chunk against the strongest selected chunk of
// the same document version, so adjacent context inherits a proportional rank.
func bestAnchor(selected []ports.Candidate, chunkID string, anchorScore map[string]float64) float64 {
	if score, ok := anchorScore[chunkID]; ok {
		return score
	}
	best := 0.0
	for _, candidate := range selected {
		if candidate.Score > best {
			best = candidate.Score
		}
	}
	return best
}

func withinWindow(ordinal int, selected []int, perSide int) bool {
	for _, candidate := range selected {
		delta := ordinal - candidate
		if delta < 0 {
			delta = -delta
		}
		if delta <= perSide {
			return true
		}
	}
	return false
}
