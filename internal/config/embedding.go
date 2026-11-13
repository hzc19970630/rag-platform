package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/configschema"
)

// ChunkConfig describes how normalized document blocks are split. Documents
// differ in shape (manuals, policies, FAQ), so chunk granularity is policy and
// belongs in configuration rather than in pipeline constants. Changing these
// values does not require a rebuild; already-ingested documents keep their
// chunks until they are explicitly reprocessed.
type ChunkConfig struct {
	// Size is the target maximum chunk length in runes.
	Size int
	// Overlap is how many runes each child chunk shares with its predecessor.
	Overlap int
	// Markers are preferred split points, tried before the fixed-size window.
	Markers []string
	// MinSize is the threshold under which a trailing chunk is merged back into
	// its predecessor instead of being indexed on its own.
	MinSize int
}

func (c ChunkConfig) Validate() error {
	return configschema.ValidateNative(configschema.DomainChunking, map[string]any{
		"size":          c.Size,
		"overlap":       c.Overlap,
		"min_size":      c.MinSize,
		"split_markers": c.Markers,
	})
}

// EmbeddingConfig describes the embedding model used by the ingest pipeline.
// The model is configuration, not code: swapping providers or models must not
// require a rebuild. Dimension is part of the contract because the vector
// index is built for exactly one dimension at a time.
type EmbeddingConfig struct {
	Source     string
	BaseURL    string
	APIKey     string
	Model      string
	Dimension  int
	BatchSize  int
	TimeoutSec int
	// HealthURL is the provider liveness endpoint used by the model catalog.
	// Empty derives it from BaseURL (/models); a non-empty value must be a URL.
	HealthURL string
	// IndexLockTimeoutSec bounds how long embedctl waits for locks while it
	// builds the chunks HNSW index CONCURRENTLY. Zero disables the timeout.
	IndexLockTimeoutSec int
	// IndexStatementTimeoutSec bounds one CREATE INDEX CONCURRENTLY statement.
	// Zero leaves it unlimited, which suits long builds on large tables.
	IndexStatementTimeoutSec int
}

func (e EmbeddingConfig) Validate(appEnv string) error {
	switch e.Source {
	case "local":
		// Offline deterministic adapter, development and acceptance only. A
		// worker running it in production would write non-semantic vectors.
		if appEnv == "production" {
			return fmt.Errorf("EMBEDDING_SOURCE=local is a development adapter and must not run in production")
		}
	case "remote":
		if strings.TrimSpace(e.BaseURL) == "" {
			return fmt.Errorf("EMBEDDING_BASE_URL is required when EMBEDDING_SOURCE=remote")
		}
		if strings.TrimSpace(e.Model) == "" {
			return fmt.Errorf("LLM_EMBEDDING_MODEL is required when EMBEDDING_SOURCE=remote")
		}
		parsed, err := url.Parse(e.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("EMBEDDING_BASE_URL must be a valid base URL")
		}
		if strings.TrimSpace(e.HealthURL) != "" {
			health, err := url.Parse(e.HealthURL)
			if err != nil || health.Scheme == "" || health.Host == "" {
				return fmt.Errorf("EMBEDDING_HEALTH_URL must be a valid URL")
			}
		}
	default:
		return fmt.Errorf("EMBEDDING_SOURCE must be local or remote, got %q", e.Source)
	}
	if e.Dimension <= 0 {
		return fmt.Errorf("EMBEDDING_DIMENSION must be a positive integer")
	}
	return nil
}
