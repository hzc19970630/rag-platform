package app

import (
	"context"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
)

type fakeSpaceResolver struct {
	space modelregistry.EmbeddingSpace
	err   error
}

func (f fakeSpaceResolver) ResolveEmbedding(context.Context, string, string) (modelregistry.EmbeddingSpace, error) {
	return f.space, f.err
}

func (f fakeSpaceResolver) ResolveEmbeddingModelSpace(context.Context, string, string) (modelregistry.EmbeddingSpace, error) {
	return f.space, f.err
}

type fakeEmbedder struct {
	name      string
	dimension int
}

func (f fakeEmbedder) Embed(context.Context, []string) ([][]float32, error) { return nil, nil }
func (f fakeEmbedder) Dimension() int                                       { return f.dimension }
func (f fakeEmbedder) Name() string                                         { return f.name }

func newRouterForTest(space modelregistry.EmbeddingSpace) *embeddingRouter {
	return newEmbeddingRouter(fakeEmbedder{name: "deploy-embed", dimension: 8}, fakeSpaceResolver{space: space}, config.Config{
		Embedding: config.EmbeddingConfig{BatchSize: 16, TimeoutSec: 5},
	})
}

func TestEmbeddingRouterUsesDeploymentAdapter(t *testing.T) {
	router := newRouterForTest(modelregistry.EmbeddingSpace{
		Binding:    embedding.Binding{Model: "deploy-embed", Dimension: 8},
		Deployment: true,
	})
	binding, adapter, err := router.ResolveEmbedding(context.Background(), "tenant-1", "kb-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if binding.Model != "deploy-embed" || binding.Dimension != 8 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if adapter.Name() != "deploy-embed" || adapter.Dimension() != 8 {
		t.Fatalf("unexpected adapter: %s/%d", adapter.Name(), adapter.Dimension())
	}
}

func TestEmbeddingRouterRejectsDeploymentMismatch(t *testing.T) {
	router := newRouterForTest(modelregistry.EmbeddingSpace{
		Binding:    embedding.Binding{Model: "other-embed", Dimension: 8},
		Deployment: true,
	})
	if _, _, err := router.ResolveEmbedding(context.Background(), "tenant-1", "kb-a"); err == nil || !strings.Contains(err.Error(), "not served by the deployment adapter") {
		t.Fatalf("expected deployment mismatch, got %v", err)
	}
}

func TestEmbeddingRouterBuildsTenantAdapter(t *testing.T) {
	router := newRouterForTest(modelregistry.EmbeddingSpace{
		Binding: embedding.Binding{Model: "tenant-embed", Dimension: 12},
		Route: modelregistry.ProviderRoute{
			ProviderID: "provider-1", Kind: modelregistry.ProviderKindOpenAICompatible,
			BaseURL: "https://embed.example/v1", Model: "tenant-embed", Dimension: 12,
		},
	})
	binding, adapter, err := router.ResolveEmbedding(context.Background(), "tenant-1", "kb-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if binding.Model != "tenant-embed" || binding.Dimension != 12 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	if adapter.Name() != "tenant-embed" || adapter.Dimension() != 12 {
		t.Fatalf("unexpected adapter: %s/%d", adapter.Name(), adapter.Dimension())
	}
}

func TestEmbeddingRouterRejectsMissingRoute(t *testing.T) {
	router := newRouterForTest(modelregistry.EmbeddingSpace{
		Binding: embedding.Binding{Model: "tenant-embed", Dimension: 12},
	})
	if _, _, err := router.ResolveEmbedding(context.Background(), "tenant-1", "kb-a"); err == nil || !strings.Contains(err.Error(), "no provider route") {
		t.Fatalf("expected missing route error, got %v", err)
	}
}
