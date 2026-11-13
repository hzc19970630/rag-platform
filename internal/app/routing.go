package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/platform/llm"
	"github.com/hzc19970630/rag-platform/internal/platform/netprobe"
	"github.com/hzc19970630/rag-platform/internal/platform/rerank"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

// providerRoutes resolves a model name to the tenant provider that serves it.
// The registry owns the data; this package owns adapter construction, which is
// why the two are separate.
type providerRoutes interface {
	ProviderForModel(ctx context.Context, tenantID, modelName string) (modelregistry.ProviderRoute, error)
}

// routingChatter sends a call to the tenant provider when the request names a
// bound model. Everything else (session titles, projections, query rewriting,
// the worker) keeps the deployment adapter because it carries no Model.
type routingChatter struct {
	fallback model.Chatter
	routes   providerRoutes

	mu    sync.Mutex
	cache map[string]*llm.RemoteChatter
}

func newRoutingChatter(fallback model.Chatter, routes providerRoutes) *routingChatter {
	return &routingChatter{fallback: fallback, routes: routes, cache: make(map[string]*llm.RemoteChatter)}
}

func (c *routingChatter) Name() string { return c.fallback.Name() }

func (c *routingChatter) Complete(ctx context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	name := strings.TrimSpace(request.Model)
	if name == "" || name == c.fallback.Name() {
		return c.fallback.Complete(ctx, request)
	}
	tenantID, ok := tenantdb.TenantFrom(ctx)
	if !ok {
		return c.fallback.Complete(ctx, request)
	}
	route, err := c.routes.ProviderForModel(ctx, tenantID, name)
	if err != nil {
		if errors.Is(err, apperror.ErrNotFound) {
			// A deployment alias (CHAT_MODELS) is served by the deployment
			// adapter; only a tenant catalog entry needs a routed provider.
			return c.fallback.Complete(ctx, request)
		}
		return model.ChatResponse{}, fmt.Errorf("route chat model %q: %w", name, err)
	}
	if route.Capability != model.CapabilityChat {
		return model.ChatResponse{}, fmt.Errorf("model %q is not a chat model", name)
	}
	adapter, err := c.adapter(route)
	if err != nil {
		return model.ChatResponse{}, err
	}
	return adapter.Complete(ctx, request)
}

func (c *routingChatter) adapter(route modelregistry.ProviderRoute) (*llm.RemoteChatter, error) {
	key := adapterCacheKey(route)
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()
	options := llm.RemoteChatterOptions{
		BaseURL: route.BaseURL, APIKey: route.APIKey, Model: route.Model,
		Timeout: 60 * time.Second, HealthURL: route.HealthURL,
	}
	if options.HealthURL == "" && route.Kind == modelregistry.ProviderKindOllama {
		options.HealthURL = strings.TrimSuffix(route.BaseURL, "/v1") + "/api/tags"
	}
	adapter, err := llm.NewRemoteChatter(options)
	if err != nil {
		return nil, fmt.Errorf("build chat adapter for provider %q: %w", route.ProviderID, err)
	}
	c.mu.Lock()
	if len(c.cache) > 64 {
		c.cache = make(map[string]*llm.RemoteChatter, 8)
	}
	c.cache[key] = adapter
	c.mu.Unlock()
	return adapter, nil
}

// routingReranker mirrors the chatter for retrieval. Rerank itself always uses
// the deployment adapter; only a knowledge-base binding selects a tenant model.
type routingReranker struct {
	fallback retrieval.Reranker
	routes   providerRoutes

	mu    sync.Mutex
	cache map[string]*rerank.RemoteReranker
}

// newRoutingReranker wraps only adapters that already accept a per-request
// model. Offline rerankers keep their historical behavior, including not
// spending the tenant model budget on a resolution they would ignore.
func newRoutingReranker(fallback retrieval.Reranker, routes providerRoutes) retrieval.Reranker {
	if _, ok := fallback.(retrieval.ModelReranker); !ok {
		return fallback
	}
	return &routingReranker{fallback: fallback, routes: routes, cache: make(map[string]*rerank.RemoteReranker)}
}

func (r *routingReranker) Name() string { return r.fallback.Name() }

func (r *routingReranker) Rerank(ctx context.Context, query string, candidates []retrieval.Candidate) ([]retrieval.Candidate, error) {
	return r.fallback.Rerank(ctx, query, candidates)
}

func (r *routingReranker) RerankWithModel(ctx context.Context, modelName, query string, candidates []retrieval.Candidate) ([]retrieval.Candidate, error) {
	name := strings.TrimSpace(modelName)
	if name == "" || name == r.fallback.Name() {
		return r.fallback.Rerank(ctx, query, candidates)
	}
	tenantID, ok := tenantdb.TenantFrom(ctx)
	if !ok {
		return r.fallback.Rerank(ctx, query, candidates)
	}
	route, err := r.routes.ProviderForModel(ctx, tenantID, name)
	if err != nil {
		if errors.Is(err, apperror.ErrNotFound) {
			return r.fallback.(retrieval.ModelReranker).RerankWithModel(ctx, name, query, candidates)
		}
		return nil, fmt.Errorf("route rerank model %q: %w", name, err)
	}
	if route.Capability != model.CapabilityRerank {
		return nil, fmt.Errorf("model %q is not a rerank model", name)
	}
	adapter, err := r.adapter(route)
	if err != nil {
		return nil, err
	}
	return adapter.RerankWithModel(ctx, route.Model, query, candidates)
}

func (r *routingReranker) adapter(route modelregistry.ProviderRoute) (*rerank.RemoteReranker, error) {
	if route.Kind != modelregistry.ProviderKindOpenAICompatible {
		return nil, fmt.Errorf("provider kind %q does not serve rerank", route.Kind)
	}
	key := adapterCacheKey(route)
	r.mu.Lock()
	if cached, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return cached, nil
	}
	r.mu.Unlock()
	adapter, err := rerank.NewRemoteReranker(rerank.RemoteRerankerOptions{
		BaseURL: route.BaseURL, APIKey: route.APIKey, Model: route.Model,
		Timeout: 60 * time.Second, HealthURL: route.HealthURL,
	})
	if err != nil {
		return nil, fmt.Errorf("build rerank adapter for provider %q: %w", route.ProviderID, err)
	}
	r.mu.Lock()
	if len(r.cache) > 64 {
		r.cache = make(map[string]*rerank.RemoteReranker, 8)
	}
	r.cache[key] = adapter
	r.mu.Unlock()
	return adapter, nil
}

// adapterCacheKey ties an adapter to the provider revision and model, so a
// catalog edit (updated_at bump) rebuilds it without a restart.
func adapterCacheKey(route modelregistry.ProviderRoute) string {
	return route.ProviderID + "@" + route.UpdatedAt.UTC().Format(time.RFC3339Nano) + "|" + route.Model
}

// probeProviderRoute is the registry's liveness hook. It checks the provider
// endpoint directly; a 401/403 means the stored credential is wrong.
func probeProviderRoute(ctx context.Context, route modelregistry.ProviderRoute) error {
	endpoint := strings.TrimSpace(route.HealthURL)
	if endpoint == "" {
		endpoint = defaultProviderHealthURL(route)
	}
	return netprobe.Check(ctx, endpoint, route.APIKey)
}

func defaultProviderHealthURL(route modelregistry.ProviderRoute) string {
	baseURL := strings.TrimRight(route.BaseURL, "/")
	if route.Kind == modelregistry.ProviderKindOllama {
		return strings.TrimSuffix(baseURL, "/v1") + "/api/tags"
	}
	return baseURL + "/models"
}
