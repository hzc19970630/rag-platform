package knowledgeprojection

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/search"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/platform/vecutil"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/status"
)

// SearchOptions bounds one Knowledge Index query. The limit is applied after
// authorization filtering so a denied page cannot consume a slot.
type SearchOptions struct {
	KnowledgeBaseID string
	Query           string
	Limit           int
}

type knowledgeCandidate struct {
	hit   KnowledgeHit
	score float64
}

const knowledgeBM25Query = `
SELECT k.wiki_page_id AS page_id,
       k.wiki_page_version_id AS version_id,
       k.knowledge_base_id AS knowledge_base_id,
       k.title AS title,
       k.content AS content,
       pdb.score(k.id) AS score
FROM knowledge_index_records k
JOIN knowledge_bases kb ON kb.id = k.knowledge_base_id
JOIN wiki_pages p ON p.id = k.wiki_page_id
WHERE k.tenant_id = ?
  AND k.knowledge_base_id = ?
  AND k.status = 'ACTIVE'
  AND kb.acl_version = k.acl_version
  AND p.status = 'PUBLISHED'
  AND p.published_version_id = k.wiki_page_version_id
  AND k.content @@@ ?
ORDER BY score DESC
LIMIT ?`

const knowledgeVectorTemplate = `
SELECT k.wiki_page_id AS page_id,
       k.wiki_page_version_id AS version_id,
       k.knowledge_base_id AS knowledge_base_id,
       k.title AS title,
       k.content AS content,
       1 - (k.embedding::halfvec(%d) <=> ?::halfvec(%d)) AS score
FROM knowledge_index_records k
JOIN knowledge_bases kb ON kb.id = k.knowledge_base_id
JOIN wiki_pages p ON p.id = k.wiki_page_id
WHERE k.tenant_id = ?
  AND k.knowledge_base_id = ?
  AND k.status = 'ACTIVE'
  AND kb.acl_version = k.acl_version
  AND p.status = 'PUBLISHED'
  AND p.published_version_id = k.wiki_page_version_id
  AND k.embedding_model = ?
  AND k.embedding_dimension = %d
  AND k.embedding IS NOT NULL
ORDER BY k.embedding::halfvec(%d) <=> ?::halfvec(%d) ASC
LIMIT ?`

// Search runs the Knowledge Index query: BM25 and page-vector recall, RRF
// fusion, then the source-level authorization re-check. Wiki pages are derived
// views, so a page is returned only when every source chunk is still the
// current Evidence version and the caller may read every one of those chunks.
func (s *Service) Search(ctx context.Context, principal authz.Principal, options SearchOptions) (SearchResult, error) {
	query := normalizeSearchQuery(options.Query)
	if query == "" {
		return SearchResult{}, apperror.Invalidf("query is required")
	}
	knowledgeBaseID := options.KnowledgeBaseID
	if knowledgeBaseID == "" {
		return SearchResult{}, apperror.Invalidf("knowledge base id is required")
	}
	scope, err := s.authorize(ctx, principal, knowledgeBaseID, "read")
	if err != nil {
		return SearchResult{}, err
	}
	knowledgeBase, err := s.loadKnowledgeBase(ctx, principal.TenantID, knowledgeBaseID)
	if err != nil {
		return SearchResult{}, apperror.ErrNotFound
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return SearchResult{}, err
	}
	if !strategy.WikiEnabled {
		return SearchResult{Query: query, Items: []KnowledgeHit{}}, nil
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	keywordHits, keywordErr := s.searchKnowledgeKeyword(ctx, principal.TenantID, knowledgeBaseID, query, limit)
	vectorHits, vectorErr := s.searchKnowledgeVector(ctx, principal.TenantID, knowledgeBaseID, query, limit)
	if keywordErr != nil && vectorErr != nil {
		return SearchResult{}, fmt.Errorf("knowledge recall failed: keyword: %v; vector: %v", keywordErr, vectorErr)
	}
	if keywordErr != nil {
		keywordHits = nil
	}
	if vectorErr != nil {
		vectorHits = nil
	}
	candidates := fuseKnowledge(keywordHits, vectorHits, 60)
	if len(candidates) == 0 {
		return SearchResult{Query: query, Items: []KnowledgeHit{}}, nil
	}

	versionIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		versionIDs = append(versionIDs, candidate.hit.VersionID)
	}
	var rows []models.WikiPageSource
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("wiki_page_version_id IN ?", versionIDs).Order("wiki_page_version_id, chunk_id").Find(&rows).Error; err != nil {
		return SearchResult{}, err
	}
	sourcesByVersion := map[string][]models.WikiPageSource{}
	for _, row := range rows {
		sourcesByVersion[row.WikiPageVersionID] = append(sourcesByVersion[row.WikiPageVersionID], row)
	}

	items := make([]KnowledgeHit, 0, len(candidates))
	for _, candidate := range candidates {
		sources := sourcesByVersion[candidate.hit.VersionID]
		if len(sources) == 0 {
			s.invalidatePage(ctx, principal.TenantID, candidate.hit.PageID)
			continue
		}
		valid, err := s.validSourceChunks(ctx, sources)
		if err != nil {
			return SearchResult{}, err
		}
		if len(valid) != len(sources) {
			s.invalidatePage(ctx, principal.TenantID, candidate.hit.PageID)
			continue
		}
		refs := make([]authz.ResourceRef, 0, len(sources))
		for _, source := range sources {
			refs = append(refs, authz.ResourceRef{
				TenantID: source.TenantID, KnowledgeBaseID: source.KnowledgeBaseID,
				ResourceID: source.ChunkID, ResourceType: "chunk",
			})
		}
		readable, err := s.authz.FilterReadable(ctx, scope, refs)
		if err != nil {
			return SearchResult{}, err
		}
		if len(readable) != len(refs) {
			// One restricted source is enough to hide the whole page: the page
			// inherits the intersection of all source permissions.
			continue
		}
		candidate.hit.Sources = sources
		items = append(items, candidate.hit)
		if len(items) == limit {
			break
		}
	}
	joinStatus, err := s.joinStatus(ctx, knowledgeBase, strategy)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Query: query, Items: items, Joined: joinStatus == JoinKnowledgeReady}, nil
}

func (s *Service) searchKnowledgeKeyword(ctx context.Context, tenantID, knowledgeBaseID, query string, limit int) ([]KnowledgeHit, error) {
	expression := search.SanitizeBM25Query(query)
	if expression == "" {
		return nil, nil
	}
	rows, err := s.db.WithContext(ctx).Raw(knowledgeBM25Query, tenantID, knowledgeBaseID, expression, limit).Rows()
	if err != nil {
		return nil, fmt.Errorf("knowledge bm25 recall: %w", err)
	}
	defer rows.Close()
	var hits []KnowledgeHit
	for rows.Next() {
		var hit KnowledgeHit
		if err := rows.Scan(&hit.PageID, &hit.VersionID, &hit.KnowledgeBaseID, &hit.Title, &hit.Content, &hit.Score); err != nil {
			return nil, fmt.Errorf("scan knowledge bm25 hit: %w", err)
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func (s *Service) searchKnowledgeVector(ctx context.Context, tenantID, knowledgeBaseID, query string, limit int) ([]KnowledgeHit, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("knowledge index has no embedding adapter")
	}
	vectors, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed knowledge query: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != s.embedder.Dimension() {
		return nil, fmt.Errorf("embed knowledge query returned an unexpected vector")
	}
	dimension := len(vectors[0])
	statement := fmt.Sprintf(knowledgeVectorTemplate, dimension, dimension, dimension, dimension, dimension)
	literal := vecutil.Literal(vectors[0])
	rows, err := s.db.WithContext(ctx).Raw(statement, literal, tenantID, knowledgeBaseID, s.embedder.Name(), literal, limit).Rows()
	if err != nil {
		return nil, fmt.Errorf("knowledge vector recall: %w", err)
	}
	defer rows.Close()
	var hits []KnowledgeHit
	for rows.Next() {
		var hit KnowledgeHit
		if err := rows.Scan(&hit.PageID, &hit.VersionID, &hit.KnowledgeBaseID, &hit.Title, &hit.Content, &hit.Score); err != nil {
			return nil, fmt.Errorf("scan knowledge vector hit: %w", err)
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func fuseKnowledge(keywordHits, vectorHits []KnowledgeHit, rrfK int) []knowledgeCandidate {
	byPage := map[string]*knowledgeCandidate{}
	merge := func(hits []KnowledgeHit) {
		for index, hit := range hits {
			candidate, exists := byPage[hit.PageID]
			if !exists {
				candidate = &knowledgeCandidate{hit: hit}
				byPage[hit.PageID] = candidate
			}
			candidate.score += 1.0 / float64(rrfK+index+1)
		}
	}
	merge(keywordHits)
	merge(vectorHits)
	result := make([]knowledgeCandidate, 0, len(byPage))
	for _, candidate := range byPage {
		candidate.hit.Score = candidate.score
		result = append(result, *candidate)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].score != result[j].score {
			return result[i].score > result[j].score
		}
		return result[i].hit.PageID < result[j].hit.PageID
	})
	return result
}

// validSourceChunks returns the ids of sources that still point at the current,
// evidence-ready version and chunk. A missing id means the page is stale.
func (s *Service) validSourceChunks(ctx context.Context, sources []models.WikiPageSource) (map[string]bool, error) {
	versionIDs := make([]string, 0, len(sources))
	chunkIDs := make([]string, 0, len(sources))
	for _, source := range sources {
		versionIDs = append(versionIDs, source.DocumentVersionID)
		chunkIDs = append(chunkIDs, source.ChunkID)
	}
	var rows []struct {
		DocumentVersionID string
		ChunkID           string
	}
	if err := s.db.WithContext(ctx).Table("wiki_page_sources s").
		Select("s.document_version_id, s.chunk_id").
		Joins("JOIN document_versions dv ON dv.id = s.document_version_id").
		Joins("JOIN documents d ON d.id = s.document_id").
		Joins("JOIN chunks c ON c.id = s.chunk_id").
		Where("s.document_version_id IN ? AND s.chunk_id IN ?", versionIDs, chunkIDs).
		Where("d.latest_version_id = s.document_version_id AND d.deleted_at IS NULL").
		Where("d.status = ? AND dv.status = ? AND c.status = ?", status.EvidenceReady, status.EvidenceReady, status.EvidenceReady).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("validate knowledge sources: %w", err)
	}
	valid := map[string]bool{}
	for _, row := range rows {
		valid[row.ChunkID] = true
	}
	return valid, nil
}

func normalizeSearchQuery(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
