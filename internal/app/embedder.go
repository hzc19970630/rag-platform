package app

import (
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/ingestion"
)

// NewEmbedder resolves the embedding adapter from configuration. The pipeline
// depends only on the ingestion.Embedder interface, so swapping local hash
// vectors for a hosted or self-hosted model is a configuration change, not a
// code change. The worker and the embedctl rebuild command resolve it through
// the same factory so they cannot drift apart.
func NewEmbedder(cfg config.Config) (ingestion.Embedder, error) {
	switch cfg.Embedding.Source {
	case "local":
		return ingestion.NewLocalEmbedder(cfg.Embedding.Dimension), nil
	case "remote":
		return ingestion.NewRemoteEmbedder(ingestion.RemoteEmbedderOptions{
			BaseURL:   cfg.Embedding.BaseURL,
			APIKey:    cfg.Embedding.APIKey,
			Model:     cfg.Embedding.Model,
			Dimension: cfg.Embedding.Dimension,
			BatchSize: cfg.Embedding.BatchSize,
			Timeout:   time.Duration(cfg.Embedding.TimeoutSec) * time.Second,
		})
	default:
		return nil, fmt.Errorf("unsupported EMBEDDING_SOURCE %q", cfg.Embedding.Source)
	}
}
