package app

import (
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/platform/rerank"
	"github.com/example/rag-platform/internal/retrieval"
)

// NewReranker resolves the reranking adapter from configuration. The retrieval
// layer depends only on the retrieval.Reranker interface, so moving between the
// offline pass-through, the offline overlap reranker and a hosted model is a
// configuration change rather than a code change.
func NewReranker(cfg config.Config) (retrieval.Reranker, error) {
	switch cfg.Rerank.Source {
	case "none":
		return retrieval.NoopReranker{}, nil
	case "lexical":
		return retrieval.LexicalReranker{}, nil
	case "remote":
		return rerank.NewRemoteReranker(rerank.RemoteRerankerOptions{
			BaseURL: cfg.Rerank.BaseURL,
			APIKey:  cfg.Rerank.APIKey,
			Model:   cfg.Rerank.Model,
			Timeout: time.Duration(cfg.Rerank.TimeoutSec) * time.Second,
		})
	default:
		return nil, fmt.Errorf("unsupported RERANK_SOURCE %q", cfg.Rerank.Source)
	}
}
