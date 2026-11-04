// Package search holds the Evidence Index adapters: the BM25 keyword index
// projection check, BM25 recall, and parent/neighbour context expansion.
package search

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/example/rag-platform/internal/platform/cjk"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
)

// BM25Searcher runs keyword recall against the Evidence Index. Scope filters
// (tenant, knowledge base, current version, evidence-ready) are part of the
// query, so recall never returns rows the caller may not read.
type BM25Searcher struct {
	db *gorm.DB
}

func NewBM25Searcher(db *gorm.DB) *BM25Searcher { return &BM25Searcher{db: db} }

const bm25RecallQuery = `
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
       pdb.score(c.id) AS score
FROM chunks c
JOIN document_versions dv ON dv.id = c.document_version_id
JOIN documents d ON d.id = dv.document_id
WHERE c.tenant_id = ?
  AND c.knowledge_base_id IN ?
  AND c.status = ?
  AND c.parent_id IS NOT NULL
  AND d.latest_version_id = c.document_version_id
  AND d.status = ?
  AND d.deleted_at IS NULL
  AND c.content @@@ ?
ORDER BY score DESC
LIMIT ?`

func (s *BM25Searcher) SearchKeyword(ctx context.Context, request retrieval.RecallRequest) ([]retrieval.Hit, error) {
	expression := SanitizeBM25Query(request.Query)
	if expression == "" || request.TopK <= 0 || len(request.KnowledgeBaseIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.WithContext(ctx).Raw(bm25RecallQuery, request.TenantID, request.KnowledgeBaseIDs, status.EvidenceReady, status.EvidenceReady, expression, request.TopK).Rows()
	if err != nil {
		return nil, fmt.Errorf("bm25 recall: %w", err)
	}
	defer rows.Close()

	var hits []retrieval.Hit
	rank := 0
	for rows.Next() {
		var hit retrieval.Hit
		if err := rows.Scan(&hit.ChunkID, &hit.DocumentID, &hit.DocumentVersionID, &hit.KnowledgeBaseID, &hit.ParentID, &hit.Ordinal, &hit.Content, &hit.ContentHash, &hit.HeadingPath, &hit.PageNumber, &hit.SourcePath, &hit.Score); err != nil {
			return nil, fmt.Errorf("scan bm25 hit: %w", err)
		}
		// The keyword threshold is compared against the raw BM25 score, which is
		// unbounded, so only a positive floor is meaningful.
		if request.Threshold > 0 && hit.Score < request.Threshold {
			continue
		}
		rank++
		hit.Rank = rank
		hit.Channel = retrieval.ChannelKeyword
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// bm25TokenPattern keeps only characters that are safe inside a ParadeDB query
// string. Everything else (quotes, parentheses, colons, operators) is dropped,
// which removes the query-injection surface of passing raw user input to @@@.
var bm25TokenPattern = regexp.MustCompile(`[\p{Han}\p{Latin}\p{N}]+`)

// bm25Reserved tokens are operators to the query parser. User input must never
// be able to inject them as operators, so they are dropped as terms.
var bm25Reserved = map[string]bool{"and": true, "or": true, "not": true}

// bm25Stopwords are function words that carry no retrieval signal. They must be
// dropped because the query is a disjunction: keeping "the" or "的" would make
// every chunk match, which in turn would make the "insufficient evidence"
// refusal path unreachable for ordinary questions.
var bm25Stopwords = map[string]bool{
	"a": true, "about": true, "after": true, "all": true, "also": true, "an": true, "any": true, "are": true,
	"as": true, "at": true, "be": true, "because": true, "been": true, "before": true, "being": true, "between": true,
	"both": true, "but": true, "by": true, "can": true, "could": true, "did": true, "do": true, "does": true,
	"during": true, "each": true, "for": true, "from": true, "had": true, "has": true, "have": true, "he": true,
	"her": true, "here": true, "his": true, "how": true, "i": true, "if": true, "in": true, "into": true,
	"is": true, "it": true, "its": true, "just": true, "may": true, "me": true, "might": true, "more": true,
	"most": true, "must": true, "my": true, "no": true, "of": true, "on": true, "once": true, "only": true,
	"other": true, "our": true, "over": true, "own": true, "same": true, "shall": true, "she": true, "should": true,
	"so": true, "some": true, "such": true, "than": true, "that": true, "the": true, "their": true, "them": true,
	"then": true, "there": true, "these": true, "they": true, "this": true, "those": true, "to": true, "too": true,
	"under": true, "us": true, "very": true, "was": true, "we": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "while": true, "who": true, "why": true, "will": true, "with": true, "would": true,
	"yes": true, "yet": true, "you": true, "your": true,
	// Chinese function words, single characters included.
	"的": true, "了": true, "和": true, "是": true, "在": true, "我": true, "有": true, "就": true, "不": true,
	"人": true, "都": true, "一": true, "个": true, "上": true, "也": true, "很": true, "到": true, "说": true,
	"要": true, "去": true, "你": true, "会": true, "着": true, "看": true, "好": true, "这": true, "那": true,
	"吗": true, "呢": true, "为": true, "与": true, "及": true, "或": true, "但": true, "而": true, "对": true,
	"从": true, "把": true, "被": true, "让": true, "给": true, "请": true, "只": true, "还": true, "又": true,
	"再": true, "更": true, "最": true, "如何": true, "什么": true, "怎么": true, "为什么": true, "可以": true,
	"需要": true, "应该": true, "哪些": true, "哪个": true, "多少": true,
}

// SanitizeBM25Query turns a user question into a disjunction of literal terms.
// Each term is restricted to a safe character set, so the resulting expression
// cannot change the query structure regardless of the input. Operator words and
// function words are dropped.
//
// A long CJK run is emitted both whole and as bigrams: Chinese is written
// without spaces, so a full-sentence token would otherwise almost never match
// the indexed text, and keyword recall would collapse to the vector channel.
func SanitizeBM25Query(query string) string {
	seen := map[string]bool{}
	var terms []string
	add := func(term string) {
		if term == "" || bm25Reserved[term] || bm25Stopwords[term] || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}
	for _, token := range bm25TokenPattern.FindAllString(query, -1) {
		lowered := strings.ToLower(token)
		add(lowered)
		if !cjk.Contains(lowered) {
			continue
		}
		for _, bigram := range cjk.Bigrams(lowered) {
			add(bigram)
		}
	}
	return strings.Join(terms, " OR ")
}
