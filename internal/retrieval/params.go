package retrieval

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/platform/param"
)

// ParamsOverride is one tier of the retrieval parameter override chain. Every
// field is optional so a tier only states what it changes:
//
//	YAML defaults -> knowledge base (database, audited) -> session (request)
type ParamsOverride struct {
	TopK                 *int     `json:"top_k,omitempty"`
	EmbeddingTopK        *int     `json:"embedding_top_k,omitempty"`
	KeywordTopK          *int     `json:"keyword_top_k,omitempty"`
	VectorThreshold      *float64 `json:"vector_threshold,omitempty"`
	KeywordThreshold     *float64 `json:"keyword_threshold,omitempty"`
	RerankEnabled        *bool    `json:"rerank_enabled,omitempty"`
	RerankTopK           *int     `json:"rerank_top_k,omitempty"`
	RerankThreshold      *float64 `json:"rerank_threshold,omitempty"`
	ContextTokenLimit    *int     `json:"context_token_limit,omitempty"`
	AdjacentExpansion    *int     `json:"adjacent_expansion,omitempty"`
	MaxAdjacentChunks    *int     `json:"max_adjacent_chunks,omitempty"`
	RRFK                 *int     `json:"rrf_k,omitempty"`
	ExpansionScoreFactor *float64 `json:"expansion_score_factor,omitempty"`
}

// Apply overlays the override onto a base parameter set.
func (o ParamsOverride) Apply(base Params) Params {
	if o.TopK != nil {
		base.TopK = *o.TopK
	}
	if o.EmbeddingTopK != nil {
		base.EmbeddingTopK = *o.EmbeddingTopK
	}
	if o.KeywordTopK != nil {
		base.KeywordTopK = *o.KeywordTopK
	}
	if o.VectorThreshold != nil {
		base.VectorThreshold = *o.VectorThreshold
	}
	if o.KeywordThreshold != nil {
		base.KeywordThreshold = *o.KeywordThreshold
	}
	if o.RerankEnabled != nil {
		base.RerankEnabled = *o.RerankEnabled
	}
	if o.RerankTopK != nil {
		base.RerankTopK = *o.RerankTopK
	}
	if o.RerankThreshold != nil {
		base.RerankThreshold = *o.RerankThreshold
	}
	if o.ContextTokenLimit != nil {
		base.ContextTokenLimit = *o.ContextTokenLimit
	}
	if o.AdjacentExpansion != nil {
		base.AdjacentExpansion = *o.AdjacentExpansion
	}
	if o.MaxAdjacentChunks != nil {
		base.MaxAdjacentChunks = *o.MaxAdjacentChunks
	}
	if o.RRFK != nil {
		base.RRFK = *o.RRFK
	}
	if o.ExpansionScoreFactor != nil {
		base.ExpansionScoreFactor = *o.ExpansionScoreFactor
	}
	return base
}

// Overlay returns a copy of the override with the non-nil fields of other
// applied. It is used when a knowledge base updates a subset of the parameters
// and the remaining values must keep their previous tier value.
func (o ParamsOverride) Overlay(other ParamsOverride) ParamsOverride {
	result := o
	if other.TopK != nil {
		result.TopK = other.TopK
	}
	if other.EmbeddingTopK != nil {
		result.EmbeddingTopK = other.EmbeddingTopK
	}
	if other.KeywordTopK != nil {
		result.KeywordTopK = other.KeywordTopK
	}
	if other.VectorThreshold != nil {
		result.VectorThreshold = other.VectorThreshold
	}
	if other.KeywordThreshold != nil {
		result.KeywordThreshold = other.KeywordThreshold
	}
	if other.RerankEnabled != nil {
		result.RerankEnabled = other.RerankEnabled
	}
	if other.RerankTopK != nil {
		result.RerankTopK = other.RerankTopK
	}
	if other.RerankThreshold != nil {
		result.RerankThreshold = other.RerankThreshold
	}
	if other.ContextTokenLimit != nil {
		result.ContextTokenLimit = other.ContextTokenLimit
	}
	if other.AdjacentExpansion != nil {
		result.AdjacentExpansion = other.AdjacentExpansion
	}
	if other.MaxAdjacentChunks != nil {
		result.MaxAdjacentChunks = other.MaxAdjacentChunks
	}
	if other.RRFK != nil {
		result.RRFK = other.RRFK
	}
	if other.ExpansionScoreFactor != nil {
		result.ExpansionScoreFactor = other.ExpansionScoreFactor
	}
	return result
}

// ValidateOverride range-checks only the fields a tier actually sets, so a
// partial update is validated without needing the global defaults.
func ValidateOverride(override ParamsOverride) error {
	return param.ValidateRetrieval(param.RetrievalValues{
		TopK:                 override.TopK,
		EmbeddingTopK:        override.EmbeddingTopK,
		KeywordTopK:          override.KeywordTopK,
		VectorThreshold:      override.VectorThreshold,
		KeywordThreshold:     override.KeywordThreshold,
		RerankTopK:           override.RerankTopK,
		RerankThreshold:      override.RerankThreshold,
		ContextTokenLimit:    override.ContextTokenLimit,
		AdjacentExpansion:    override.AdjacentExpansion,
		MaxAdjacentChunks:    override.MaxAdjacentChunks,
		RRFK:                 override.RRFK,
		ExpansionScoreFactor: override.ExpansionScoreFactor,
	})
}

// Validate range-checks a fully resolved parameter set. It delegates to the
// same schema as ValidateOverride so the two tiers cannot drift.
func (p Params) Validate() error {
	return param.ValidateRetrieval(param.RetrievalValues{
		TopK:                 &p.TopK,
		EmbeddingTopK:        &p.EmbeddingTopK,
		KeywordTopK:          &p.KeywordTopK,
		VectorThreshold:      &p.VectorThreshold,
		KeywordThreshold:     &p.KeywordThreshold,
		RerankTopK:           &p.RerankTopK,
		RerankThreshold:      &p.RerankThreshold,
		ContextTokenLimit:    &p.ContextTokenLimit,
		AdjacentExpansion:    &p.AdjacentExpansion,
		MaxAdjacentChunks:    &p.MaxAdjacentChunks,
		RRFK:                 &p.RRFK,
		ExpansionScoreFactor: &p.ExpansionScoreFactor,
	})
}

// ParseOverride decodes a stored or request-supplied override. An empty value
// is a valid no-op tier.
func ParseOverride(raw string) (ParamsOverride, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return ParamsOverride{}, nil
	}
	var override ParamsOverride
	if err := json.Unmarshal([]byte(raw), &override); err != nil {
		return ParamsOverride{}, apperror.Invalidf("decode retrieval parameters: %w", err)
	}
	return override, nil
}

// ResolveParams applies the database tier and then the session tier on top of
// the global defaults, and validates the result. Validation runs last so a
// session override cannot smuggle a value outside the documented ranges.
func ResolveParams(global Params, knowledgeBaseRaw, sessionRaw string) (Params, error) {
	resolved := global
	knowledgeBaseOverride, err := ParseOverride(knowledgeBaseRaw)
	if err != nil {
		return Params{}, fmt.Errorf("knowledge base retrieval config: %w", err)
	}
	resolved = knowledgeBaseOverride.Apply(resolved)

	sessionOverride, err := ParseOverride(sessionRaw)
	if err != nil {
		return Params{}, err
	}
	resolved = sessionOverride.Apply(resolved)

	if err := resolved.Validate(); err != nil {
		return Params{}, err
	}
	return resolved, nil
}

// IndexingStrategy mirrors IndexingStrategyV1: the per-knowledge-base capability
// switches. Wiki and graph stay false in M3.
type IndexingStrategy struct {
	VectorEnabled        bool     `json:"vector_enabled"`
	KeywordEnabled       bool     `json:"keyword_enabled"`
	WikiEnabled          bool     `json:"wiki_enabled"`
	GraphEnabled         bool     `json:"graph_enabled"`
	RequiredCapabilities []string `json:"required_capabilities"`
}

// DefaultIndexingStrategy keeps the Evidence Index hybrid-enabled and the
// projection capabilities off.
func DefaultIndexingStrategy() IndexingStrategy {
	return IndexingStrategy{
		VectorEnabled:        true,
		KeywordEnabled:       true,
		RequiredCapabilities: []string{"evidence"},
	}
}

// ParseIndexingStrategy decodes the stored strategy, falling back to the
// evidence-only default when the column is empty.
func ParseIndexingStrategy(raw string) (IndexingStrategy, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return DefaultIndexingStrategy(), nil
	}
	strategy := DefaultIndexingStrategy()
	if err := json.Unmarshal([]byte(raw), &strategy); err != nil {
		return IndexingStrategy{}, fmt.Errorf("decode indexing strategy: %w", err)
	}
	if len(strategy.RequiredCapabilities) == 0 {
		strategy.RequiredCapabilities = []string{"evidence"}
	}
	return strategy, nil
}
