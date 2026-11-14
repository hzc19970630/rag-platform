// Package embedding is the provider-neutral vector-space contract shared by
// ingestion, retrieval and projection. Keeping the binding and the embedder in
// one place means a knowledge base cannot be written with one model and queried
// with another: both sides resolve the same binding through one resolver.
package embedding

import (
	"context"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
)

// Binding is the vector space a knowledge base is pinned to. The pair is the
// data contract: changing either requires a managed migration, never a save.
type Binding = model.EmbeddingBinding

// Embedder produces vectors for one binding. Adapters implement it
// structurally; the domain packages depend only on this interface.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension reports the vector length this adapter produces.
	Dimension() int
	// Name identifies the model so vectors from different models are never
	// compared.
	Name() string
}

// Resolver returns the exact embedder and binding for one knowledge base.
//
// Implementations must not fall back to another model: unlike chat or rerank,
// substituting an embedding model silently mixes two vector spaces. A missing
// or unhealthy provider is an error the caller must surface.
type Resolver interface {
	ResolveEmbedding(ctx context.Context, tenantID, knowledgeBaseID string) (Binding, Embedder, error)
}
