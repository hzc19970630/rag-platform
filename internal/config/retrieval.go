package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/configschema"
	"gopkg.in/yaml.v3"
)

// RetrievalConfig is the global tier of retrieval parameters. The values are
// overridable per knowledge base (database, audited) and per session (request),
// so they are policy rather than constants.
type RetrievalConfig struct {
	TopK             int     `yaml:"top_k"`
	EmbeddingTopK    int     `yaml:"embedding_top_k"`
	KeywordTopK      int     `yaml:"keyword_top_k"`
	VectorThreshold  float64 `yaml:"vector_threshold"`
	KeywordThreshold float64 `yaml:"keyword_threshold"`
	RerankEnabled    bool    `yaml:"rerank_enabled"`
	RerankTopK       int     `yaml:"rerank_top_k"`
	RerankThreshold  float64 `yaml:"rerank_threshold"`
	// VectorEFSearch is the pgvector HNSW candidate-list size. Zero leaves the
	// server default (40 on pgvector 0.8) in place.
	VectorEFSearch int `yaml:"vector_ef_search"`
	// VectorOversample multiplies the SQL LIMIT so a selective tenant filter
	// can still fill embedding_top_k after filtering.
	VectorOversample int `yaml:"vector_oversample"`
	// VectorIterativeScan enables pgvector's relaxed-order iterative scan. It
	// only affects filtered HNSW scans; exact scans ignore it.
	VectorIterativeScan bool `yaml:"vector_iterative_scan"`
	ContextTokenLimit   int  `yaml:"context_token_limit"`
	AdjacentExpansion   int  `yaml:"adjacent_expansion"`
	MaxAdjacentChunks   int  `yaml:"max_adjacent_chunks"`
	// RRFK is the reciprocal rank fusion constant. It damps the influence of the
	// top ranks so neither recall channel dominates purely because its score
	// scale differs, which also fixes the band the fused score can land in.
	RRFK int `yaml:"rrf_k"`
	// ExpansionScoreFactor keeps expanded context ranked below the chunk that
	// pulled it in, so a parent paragraph never outranks a precise match.
	ExpansionScoreFactor float64 `yaml:"expansion_score_factor"`
}

type retrievalFile struct {
	Retrieval RetrievalConfig `yaml:"retrieval"`
}

// retrievalFileKeys records the keys the YAML file actually states. It is
// decoded separately because the typed struct cannot distinguish an omitted key
// from one explicitly set to the default.
type retrievalFileKeys struct {
	Retrieval map[string]any `yaml:"retrieval"`
}

// Validate enforces the documented ranges. Every tier goes through this check,
// so a session override cannot escape the same bounds as a knowledge-base one.
func (r RetrievalConfig) Validate() error {
	return configschema.ValidateNative(configschema.DomainRetrieval, map[string]any{
		"top_k":                  r.TopK,
		"embedding_top_k":        r.EmbeddingTopK,
		"keyword_top_k":          r.KeywordTopK,
		"vector_threshold":       r.VectorThreshold,
		"keyword_threshold":      r.KeywordThreshold,
		"rerank_enabled":         r.RerankEnabled,
		"rerank_top_k":           r.RerankTopK,
		"rerank_threshold":       r.RerankThreshold,
		"vector_ef_search":       r.VectorEFSearch,
		"vector_oversample":      r.VectorOversample,
		"vector_iterative_scan":  r.VectorIterativeScan,
		"context_token_limit":    r.ContextTokenLimit,
		"adjacent_expansion":     r.AdjacentExpansion,
		"max_adjacent_chunks":    r.MaxAdjacentChunks,
		"rrf_k":                  r.RRFK,
		"expansion_score_factor": r.ExpansionScoreFactor,
	})
}

// defaultRetrievalConfig mirrors config/config.yaml. The file is the operator
// facing tier; these values are only the fallback used when the file is absent
// so a development stack still starts. The values come from configschema so the
// safe defaults have one declaration.
func defaultRetrievalConfig() RetrievalConfig {
	return RetrievalConfig{
		TopK:             configschema.IntDefault("top_k"),
		EmbeddingTopK:    configschema.IntDefault("embedding_top_k"),
		KeywordTopK:      configschema.IntDefault("keyword_top_k"),
		VectorThreshold:  configschema.FloatDefault("vector_threshold"),
		KeywordThreshold: configschema.FloatDefault("keyword_threshold"),
		RerankEnabled:    configschema.BoolDefault("rerank_enabled"),
		RerankTopK:       configschema.IntDefault("rerank_top_k"),
		RerankThreshold:  configschema.FloatDefault("rerank_threshold"),
		// The recall probe (scripts/vector_recall_probe.py) measured that
		// over-sampling alone cannot fill a small tenant's HNSW result; relaxed
		// iterative scan can. Zero ef_search keeps pgvector's default (40).
		VectorEFSearch:      configschema.IntDefault("vector_ef_search"),
		VectorOversample:    configschema.IntDefault("vector_oversample"),
		VectorIterativeScan: configschema.BoolDefault("vector_iterative_scan"),
		ContextTokenLimit:   configschema.IntDefault("context_token_limit"),
		AdjacentExpansion:   configschema.IntDefault("adjacent_expansion"),
		MaxAdjacentChunks:   configschema.IntDefault("max_adjacent_chunks"),
		// With rrf_k = 60 a chunk ranked first by both channels fuses to
		// 2/(60+1) ~ 0.033, which is the band the API reports as `score`.
		RRFK:                 configschema.IntDefault("rrf_k"),
		ExpansionScoreFactor: configschema.FloatDefault("expansion_score_factor"),
	}
}

// applyRetrievalDefaults reads the global retrieval parameter file. A missing
// file keeps the code defaults; a malformed one is reported rather than
// silently ignored.
func applyRetrievalDefaults(cfg *Config) error {
	path := strings.TrimSpace(cfg.RetrievalConfigFile)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read retrieval config %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	cfg.RetrievalFileHash = hex.EncodeToString(sum[:])
	var parsed retrievalFile
	// Seed with the code defaults so a partial file only overrides the keys it
	// actually states, instead of zeroing the rest.
	parsed.Retrieval = defaultRetrievalConfig()
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse retrieval config %s: %w", path, err)
	}
	var stated retrievalFileKeys
	if err := yaml.Unmarshal(raw, &stated); err != nil {
		return fmt.Errorf("parse retrieval config %s: %w", path, err)
	}
	if stated.Retrieval != nil {
		if err := configschema.ValidateNative(configschema.DomainRetrieval, stated.Retrieval); err != nil {
			return fmt.Errorf("validate retrieval config %s: %w", path, err)
		}
		cfg.RetrievalFileKeys = map[string]bool{}
		for key := range stated.Retrieval {
			cfg.RetrievalFileKeys[key] = true
		}
	}
	cfg.Retrieval = parsed.Retrieval
	return cfg.Retrieval.Validate()
}
