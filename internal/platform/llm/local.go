package llm

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/platform/cjk"
	"github.com/hzc19970630/rag-platform/internal/platform/token"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
)

// LocalChatter is a deterministic, offline generation adapter for local
// development and acceptance runs. It is not a language model: it selects the
// evidence sentences that literally contain query terms and cites them. It
// returns an empty answer when no evidence supports the question, which lets
// the application layer apply the fallback template. It is rejected in
// production by configuration validation.
type LocalChatter struct {
	maxSentences int
}

func NewLocalChatter() *LocalChatter { return &LocalChatter{maxSentences: 3} }

func (c *LocalChatter) Name() string { return "local-extractive" }

var sentenceSplitter = regexp.MustCompile(`[。！？!?\.\n]+`)

func (c *LocalChatter) Complete(ctx context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	terms := queryTerms(request.Query)
	if len(terms) == 0 || len(request.Evidence) == 0 {
		return model.ChatResponse{Text: "", Model: c.Name()}, nil
	}
	type scored struct {
		index int
		score int
		text  string
	}
	var candidates []scored
	for _, item := range request.Evidence {
		for _, sentence := range sentenceSplitter.Split(item.Text, -1) {
			sentence = strings.TrimSpace(sentence)
			if sentence == "" {
				continue
			}
			lowered := strings.ToLower(sentence)
			score := 0
			for _, term := range terms {
				if strings.Contains(lowered, term) {
					score++
				}
			}
			if score == 0 {
				continue
			}
			if heading := strings.ToLower(strings.TrimSpace(item.HeadingPath)); heading != "" {
				for _, term := range terms {
					if strings.Contains(heading, term) {
						score++
					}
				}
			}
			candidates = append(candidates, scored{index: item.Index, score: score, text: sentence})
		}
	}
	if len(candidates) == 0 {
		return model.ChatResponse{Text: "", Model: c.Name()}, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].index < candidates[j].index
	})
	limit := c.maxSentences
	if len(candidates) < limit {
		limit = len(candidates)
	}
	parts := make([]string, 0, limit)
	for _, candidate := range candidates[:limit] {
		parts = append(parts, candidate.text+" ["+itoa(candidate.index)+"]")
	}
	text := strings.Join(parts, " ")
	return model.ChatResponse{Text: text, Model: c.Name(), CompletionTokens: token.Estimate(text)}, nil
}

var termPattern = regexp.MustCompile(`[\p{Han}\p{Latin}\p{N}]+`)

// queryTerms extracts lowercase retrieval terms. CJK runs are kept whole and
// additionally split into bigrams so a longer question still matches shorter
// Chinese phrases.
func queryTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(term string) {
		term = strings.ToLower(strings.TrimSpace(term))
		if len([]rune(term)) < 2 && !cjk.Contains(term) {
			return
		}
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}
	for _, token := range termPattern.FindAllString(query, -1) {
		if cjk.Contains(token) {
			add(token)
			for _, bigram := range cjk.Bigrams(token) {
				add(bigram)
			}
			continue
		}
		add(token)
	}
	return terms
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
