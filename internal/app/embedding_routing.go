package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
)

// embeddingSpaceResolver is the registry subset the router needs. Keeping it
// narrow makes the router independent of catalog writes and health probes.
type embeddingSpaceResolver interface {
	ResolveEmbedding(ctx context.Context, tenantID, knowledgeBaseID string) (modelregistry.EmbeddingSpace, error)
	ResolveEmbeddingModelSpace(ctx context.Context, tenantID, modelName string) (modelregistry.EmbeddingSpace, error)
}

// embeddingRouter resolves the exact adapter for a knowledge base's vector
// space. Deployment bindings use the in-process adapter; tenant entries build a
// remote adapter pinned to the provider revision. It never falls back: a
// substitute model would write or query a different vector space.
type embeddingRouter struct {
	fallback  embedding.Embedder
	spaces    embeddingSpaceResolver
	batchSize int
	timeout   time.Duration

	mu    sync.Mutex
	cache map[string]*ingestion.RemoteEmbedder
}

func newEmbeddingRouter(fallback embedding.Embedder, spaces embeddingSpaceResolver, cfg config.Config) *embeddingRouter {
	return &embeddingRouter{
		fallback:  fallback,
		spaces:    spaces,
		batchSize: cfg.Embedding.BatchSize,
		timeout:   time.Duration(cfg.Embedding.TimeoutSec) * time.Second,
		cache:     make(map[string]*ingestion.RemoteEmbedder),
	}
}

func (r *embeddingRouter) ResolveEmbedding(ctx context.Context, tenantID, knowledgeBaseID string) (embedding.Binding, embedding.Embedder, error) {
	space, err := r.spaces.ResolveEmbedding(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		return embedding.Binding{}, nil, fmt.Errorf("resolve embedding for knowledge base %s: %w", knowledgeBaseID, err)
	}
	binding, adapter, err := r.resolveSpace(space)
	if err != nil {
		return embedding.Binding{}, nil, fmt.Errorf("knowledge base %s: %w", knowledgeBaseID, err)
	}
	return binding, adapter, nil
}

// EmbedderForModel builds the adapter for a model name that is not (yet) the
// knowledge-base binding. It is what an embedding migration embeds into.
func (r *embeddingRouter) EmbedderForModel(ctx context.Context, tenantID, modelName string) (embedding.Binding, embedding.Embedder, error) {
	space, err := r.spaces.ResolveEmbeddingModelSpace(ctx, tenantID, modelName)
	if err != nil {
		return embedding.Binding{}, nil, err
	}
	return r.resolveSpace(space)
}

func (r *embeddingRouter) resolveSpace(space modelregistry.EmbeddingSpace) (embedding.Binding, embedding.Embedder, error) {
	if space.Deployment {
		if r.fallback == nil {
			return embedding.Binding{}, nil, fmt.Errorf("embedding adapter is not configured")
		}
		if space.Binding.Model != r.fallback.Name() || space.Binding.Dimension != r.fallback.Dimension() {
			return embedding.Binding{}, nil, fmt.Errorf(
				"model %s/%d is not served by the deployment adapter (%s/%d)",
				space.Binding.Model, space.Binding.Dimension, r.fallback.Name(), r.fallback.Dimension(),
			)
		}
		return space.Binding, r.fallback, nil
	}
	adapter, err := r.adapter(space)
	if err != nil {
		return embedding.Binding{}, nil, err
	}
	if adapter.Dimension() != space.Binding.Dimension {
		return embedding.Binding{}, nil, fmt.Errorf(
			"embedding provider for model %q returned dimension %d, binding requires %d",
			space.Binding.Model, adapter.Dimension(), space.Binding.Dimension,
		)
	}
	return space.Binding, adapter, nil
}

func (r *embeddingRouter) adapter(space modelregistry.EmbeddingSpace) (*ingestion.RemoteEmbedder, error) {
	route := space.Route
	if route.ProviderID == "" {
		return nil, fmt.Errorf("embedding model %q has no provider route", space.Binding.Model)
	}
	key := adapterCacheKey(route) + fmt.Sprintf("|%d", space.Binding.Dimension)
	r.mu.Lock()
	if cached, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return cached, nil
	}
	r.mu.Unlock()
	adapter, err := ingestion.NewRemoteEmbedder(ingestion.RemoteEmbedderOptions{
		BaseURL:   route.BaseURL,
		APIKey:    route.APIKey,
		Model:     route.Model,
		Dimension: space.Binding.Dimension,
		BatchSize: r.batchSize,
		Timeout:   r.timeout,
		HealthURL: route.HealthURL,
	})
	if err != nil {
		return nil, fmt.Errorf("build embedding adapter for provider %q: %w", route.ProviderID, err)
	}
	r.mu.Lock()
	if len(r.cache) > 64 {
		r.cache = make(map[string]*ingestion.RemoteEmbedder, 8)
	}
	r.cache[key] = adapter
	r.mu.Unlock()
	return adapter, nil
}

var _ embedding.Resolver = (*embeddingRouter)(nil)
