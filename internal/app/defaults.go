package app

import (
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/retrieval"
)

// retrievalDefaults converts the YAML tier into the retrieval domain's own
// parameter type at the composition boundary.
func retrievalDefaults(cfg config.RetrievalConfig) retrieval.Params {
	return retrieval.Params{
		TopK:                 cfg.TopK,
		EmbeddingTopK:        cfg.EmbeddingTopK,
		KeywordTopK:          cfg.KeywordTopK,
		VectorThreshold:      cfg.VectorThreshold,
		KeywordThreshold:     cfg.KeywordThreshold,
		RerankEnabled:        cfg.RerankEnabled,
		RerankTopK:           cfg.RerankTopK,
		RerankThreshold:      cfg.RerankThreshold,
		VectorEFSearch:       cfg.VectorEFSearch,
		VectorOversample:     cfg.VectorOversample,
		VectorIterativeScan:  cfg.VectorIterativeScan,
		ContextTokenLimit:    cfg.ContextTokenLimit,
		AdjacentExpansion:    cfg.AdjacentExpansion,
		MaxAdjacentChunks:    cfg.MaxAdjacentChunks,
		RRFK:                 cfg.RRFK,
		ExpansionScoreFactor: cfg.ExpansionScoreFactor,
	}
}
