package retrieval

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/configschema"
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
	VectorEFSearch       *int     `json:"vector_ef_search,omitempty"`
	VectorOversample     *int     `json:"vector_oversample,omitempty"`
	VectorIterativeScan  *bool    `json:"vector_iterative_scan,omitempty"`
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
	if o.VectorEFSearch != nil {
		base.VectorEFSearch = *o.VectorEFSearch
	}
	if o.VectorOversample != nil {
		base.VectorOversample = *o.VectorOversample
	}
	if o.VectorIterativeScan != nil {
		base.VectorIterativeScan = *o.VectorIterativeScan
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
	if other.VectorEFSearch != nil {
		result.VectorEFSearch = other.VectorEFSearch
	}
	if other.VectorOversample != nil {
		result.VectorOversample = other.VectorOversample
	}
	if other.VectorIterativeScan != nil {
		result.VectorIterativeScan = other.VectorIterativeScan
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
	values := map[string]any{}
	putInt := func(key string, value *int) {
		if value != nil {
			values[key] = *value
		}
	}
	putFloat := func(key string, value *float64) {
		if value != nil {
			values[key] = *value
		}
	}
	putInt("top_k", override.TopK)
	putInt("embedding_top_k", override.EmbeddingTopK)
	putInt("keyword_top_k", override.KeywordTopK)
	putFloat("vector_threshold", override.VectorThreshold)
	putFloat("keyword_threshold", override.KeywordThreshold)
	if override.RerankEnabled != nil {
		values["rerank_enabled"] = *override.RerankEnabled
	}
	putInt("rerank_top_k", override.RerankTopK)
	putFloat("rerank_threshold", override.RerankThreshold)
	putInt("vector_ef_search", override.VectorEFSearch)
	putInt("vector_oversample", override.VectorOversample)
	if override.VectorIterativeScan != nil {
		values["vector_iterative_scan"] = *override.VectorIterativeScan
	}
	putInt("context_token_limit", override.ContextTokenLimit)
	putInt("adjacent_expansion", override.AdjacentExpansion)
	putInt("max_adjacent_chunks", override.MaxAdjacentChunks)
	putInt("rrf_k", override.RRFK)
	putFloat("expansion_score_factor", override.ExpansionScoreFactor)
	return configschema.ValidateNative(configschema.DomainRetrieval, values)
}

// Validate range-checks a fully resolved parameter set. It delegates to the
// same schema as ValidateOverride so the two tiers cannot drift. A zero
// VectorOversample is the Go zero value ("unset"); the vector adapter treats it
// as 1, so it is not rejected here. Override and YAML tiers still require 1..20.
func (p Params) Validate() error {
	values := map[string]any{
		"top_k":                  p.TopK,
		"embedding_top_k":        p.EmbeddingTopK,
		"keyword_top_k":          p.KeywordTopK,
		"vector_threshold":       p.VectorThreshold,
		"keyword_threshold":      p.KeywordThreshold,
		"rerank_enabled":         p.RerankEnabled,
		"rerank_top_k":           p.RerankTopK,
		"rerank_threshold":       p.RerankThreshold,
		"vector_ef_search":       p.VectorEFSearch,
		"vector_iterative_scan":  p.VectorIterativeScan,
		"context_token_limit":    p.ContextTokenLimit,
		"adjacent_expansion":     p.AdjacentExpansion,
		"max_adjacent_chunks":    p.MaxAdjacentChunks,
		"rrf_k":                  p.RRFK,
		"expansion_score_factor": p.ExpansionScoreFactor,
	}
	if p.VectorOversample > 0 {
		values["vector_oversample"] = p.VectorOversample
	}
	return configschema.ValidateNative(configschema.DomainRetrieval, values)
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

// ResolveParams applies the database tiers and then the session tier on top of
// the global defaults, and validates the result. Validation runs last so a
// session override cannot smuggle a value outside the documented ranges.
func ResolveParams(global Params, knowledgeBaseRaw, sessionRaw string) (Params, error) {
	return ResolveParamsWithTenant(global, "", knowledgeBaseRaw, sessionRaw)
}

// ResolveParamsWithTenant applies the tenant, knowledge-base and session tiers
// in order. The tenant tier is the middle policy layer: it can set a default
// for every knowledge base, while a knowledge base can still override it.
func ResolveParamsWithTenant(global Params, tenantRaw, knowledgeBaseRaw, sessionRaw string) (Params, error) {
	resolved := global
	tenantOverride, err := ParseOverride(tenantRaw)
	if err != nil {
		return Params{}, fmt.Errorf("tenant retrieval config: %w", err)
	}
	resolved = tenantOverride.Apply(resolved)

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
