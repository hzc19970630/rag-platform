package retrieval

import (
	"context"
	"strings"
)

// NoopReranker preserves the fused order. It is the default so that enabling
// rerank is an explicit, evaluated decision rather than an implicit dependency
// on a model that may not be configured.
type NoopReranker struct{}

func (NoopReranker) Name() string { return "none" }

func (NoopReranker) Rerank(_ context.Context, _ string, candidates []Candidate) ([]Candidate, error) {
	return candidates, nil
}

// LexicalReranker is a deterministic offline reranker: it scores candidates by
// the fraction of query terms they contain and orders by that score. It exists
// so the rerank capability can be exercised and evaluated without a provider. A
// model-based reranker can replace it behind the same port. The fused score is
// left untouched: callers report it, and only the ordering changes.
type LexicalReranker struct{}

func (LexicalReranker) Name() string { return "lexical-overlap" }

func (LexicalReranker) Rerank(_ context.Context, query string, candidates []Candidate) ([]Candidate, error) {
	terms := rerankTerms(query)
	if len(terms) == 0 {
		return candidates, nil
	}
	scored := make([]Candidate, len(candidates))
	copy(scored, candidates)
	for index := range scored {
		haystack := strings.ToLower(scored[index].Content + " " + scored[index].HeadingPath)
		matches := 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				matches++
			}
		}
		// Normalized to 0-1 so rerank_threshold is comparable across adapters.
		value := float64(matches) / float64(len(terms))
		scored[index].RerankScore = &value
		if !contains(scored[index].Channels, ChannelReranked) {
			scored[index].Channels = append(scored[index].Channels, ChannelReranked)
		}
	}
	SortCandidates(scored)
	return scored, nil
}

// orderScore is the single ranking key for a candidate: once a reranker has
// scored it that score decides the order, otherwise the fused score does. Having
// one key means expansion and the final sort cannot silently undo rerank order,
// and it keeps the fused score meaningful for the caller that reports it.
//
// The two scales are not comparable, which is why a reranker must score every
// candidate it is handed: an unscored candidate would otherwise be compared
// against rerank scores on a different scale. Adapters reject an empty result,
// and an unscored survivor of a partial response sorts below every endorsed one.
func orderScore(candidate Candidate) float64 {
	if candidate.RerankScore != nil {
		return *candidate.RerankScore
	}
	return candidate.Score
}

var rerankTermPattern = strings.NewReplacer(",", " ", ".", " ", "。", " ", "！", " ", "？", " ", "!", " ", "?", " ", ":", " ", ";", " ", "(", " ", ")", " ", "[", " ", "]", " ", "\n", " ", "\t", " ")

func rerankTerms(query string) []string {
	normalized := rerankTermPattern.Replace(strings.ToLower(query))
	seen := map[string]bool{}
	var terms []string
	for _, token := range strings.Fields(normalized) {
		if len([]rune(token)) < 2 || seen[token] {
			continue
		}
		seen[token] = true
		terms = append(terms, token)
	}
	return terms
}
