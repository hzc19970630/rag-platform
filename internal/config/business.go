package config

import (
	"github.com/hzc19970630/rag-platform/internal/configschema"
)

// RetrievalValues exposes the deployment retrieval tier as schema-keyed values.
// It is the single conversion point between the typed config and the generic
// configuration surface.
func RetrievalValues(cfg RetrievalConfig) map[string]any {
	return map[string]any{
		"top_k":                  cfg.TopK,
		"embedding_top_k":        cfg.EmbeddingTopK,
		"keyword_top_k":          cfg.KeywordTopK,
		"vector_threshold":       cfg.VectorThreshold,
		"keyword_threshold":      cfg.KeywordThreshold,
		"rerank_enabled":         cfg.RerankEnabled,
		"rerank_top_k":           cfg.RerankTopK,
		"rerank_threshold":       cfg.RerankThreshold,
		"vector_ef_search":       cfg.VectorEFSearch,
		"vector_oversample":      cfg.VectorOversample,
		"vector_iterative_scan":  cfg.VectorIterativeScan,
		"context_token_limit":    cfg.ContextTokenLimit,
		"adjacent_expansion":     cfg.AdjacentExpansion,
		"max_adjacent_chunks":    cfg.MaxAdjacentChunks,
		"rrf_k":                  cfg.RRFK,
		"expansion_score_factor": cfg.ExpansionScoreFactor,
	}
}

// ChunkingValues exposes the deployment chunking tier as schema-keyed values.
func ChunkingValues(cfg ChunkConfig) map[string]any {
	return map[string]any{
		"size":          cfg.Size,
		"overlap":       cfg.Overlap,
		"min_size":      cfg.MinSize,
		"split_markers": cfg.Markers,
	}
}

// RetrievalTier is the deployment layer of the retrieval override chain.
func (c Config) RetrievalTier() configschema.Tier {
	explicit := c.RetrievalFileKeys
	if explicit == nil {
		explicit = map[string]bool{}
	}
	return configschema.NewTier(RetrievalValues(c.Retrieval), explicit)
}

// ChunkingTier is the deployment layer of the chunking override chain.
func (c Config) ChunkingTier() configschema.Tier {
	explicit := c.ChunkEnvKeys
	if explicit == nil {
		explicit = map[string]bool{}
	}
	return configschema.NewTier(ChunkingValues(c.Chunk), explicit)
}
