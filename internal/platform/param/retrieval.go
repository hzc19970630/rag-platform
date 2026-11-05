package param

// RetrievalValues is the shared range schema for retrieval parameters. Nil
// fields are not checked, so the same schema validates a complete parameter
// set and a partial override tier.
type RetrievalValues struct {
	TopK                 *int
	EmbeddingTopK        *int
	KeywordTopK          *int
	VectorThreshold      *float64
	KeywordThreshold     *float64
	RerankTopK           *int
	RerankThreshold      *float64
	ContextTokenLimit    *int
	AdjacentExpansion    *int
	MaxAdjacentChunks    *int
	RRFK                 *int
	ExpansionScoreFactor *float64
}

// ValidateRetrieval is the single source of truth for retrieval parameter
// ranges used by YAML defaults, knowledge-base tiers and session overrides.
func ValidateRetrieval(values RetrievalValues) error {
	for name, entry := range map[string]*int{
		"top_k":           values.TopK,
		"embedding_top_k": values.EmbeddingTopK,
		"keyword_top_k":   values.KeywordTopK,
		"rerank_top_k":    values.RerankTopK,
	} {
		if entry != nil {
			if err := IntRange(name, *entry, 0, 200); err != nil {
				return err
			}
		}
	}
	if values.VectorThreshold != nil {
		if err := FloatRange("vector_threshold", *values.VectorThreshold, 0, 1); err != nil {
			return err
		}
	}
	if values.KeywordThreshold != nil {
		if err := NonNegativeFloat("keyword_threshold", *values.KeywordThreshold); err != nil {
			return err
		}
	}
	if values.RerankThreshold != nil {
		if err := FloatRange("rerank_threshold", *values.RerankThreshold, -10, 10); err != nil {
			return err
		}
	}
	if values.ContextTokenLimit != nil {
		if err := PositiveInt("context_token_limit", *values.ContextTokenLimit); err != nil {
			return err
		}
	}
	if values.AdjacentExpansion != nil {
		if err := NonNegativeInt("adjacent_expansion", *values.AdjacentExpansion); err != nil {
			return err
		}
	}
	if values.MaxAdjacentChunks != nil {
		if err := NonNegativeInt("max_adjacent_chunks", *values.MaxAdjacentChunks); err != nil {
			return err
		}
	}
	if values.RRFK != nil {
		if err := IntRange("rrf_k", *values.RRFK, 1, 200); err != nil {
			return err
		}
	}
	if values.ExpansionScoreFactor != nil {
		if err := FloatRange("expansion_score_factor", *values.ExpansionScoreFactor, 0, 1); err != nil {
			return err
		}
	}
	return nil
}
