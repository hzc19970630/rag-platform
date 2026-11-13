package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/platform/rerank"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

type staticRoutes map[string]modelregistry.ProviderRoute

func (s staticRoutes) ProviderForModel(_ context.Context, _ string, name string) (modelregistry.ProviderRoute, error) {
	if route, ok := s[name]; ok {
		return route, nil
	}
	return modelregistry.ProviderRoute{}, apperror.ErrNotFound
}

type stubChatter struct {
	name      string
	lastModel string
}

func (s *stubChatter) Name() string { return s.name }

func (s *stubChatter) Complete(_ context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	s.lastModel = request.Model
	return model.ChatResponse{Text: "deployment", Model: s.name}, nil
}

func TestRoutingChatterKeepsDeploymentAliases(t *testing.T) {
	fallback := &stubChatter{name: "deploy-chat"}
	chatter := newRoutingChatter(fallback, staticRoutes{})
	ctx := tenantdb.WithTenant(context.Background(), "tenant-1")
	response, err := chatter.Complete(ctx, model.ChatRequest{Model: "chat-b", Query: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if response.Text != "deployment" || fallback.lastModel != "chat-b" {
		t.Fatalf("expected the deployment adapter to serve the alias, got %+v model=%q", response, fallback.lastModel)
	}
}

func TestRoutingChatterDispatchesToTenantProvider(t *testing.T) {
	var gotModel, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		gotModel = payload.Model
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"tenant-chat","choices":[{"message":{"role":"assistant","content":"tenant answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer server.Close()

	fallback := &stubChatter{name: "deploy-chat"}
	routes := staticRoutes{"tenant-chat": {
		ProviderID: "provider-1", Kind: modelregistry.ProviderKindOpenAICompatible,
		BaseURL: server.URL, APIKey: "sk-test", Model: "tenant-chat",
		Capability: model.CapabilityChat, UpdatedAt: time.Now(),
	}}
	chatter := newRoutingChatter(fallback, routes)
	ctx := tenantdb.WithTenant(context.Background(), "tenant-1")

	response, err := chatter.Complete(ctx, model.ChatRequest{Model: "tenant-chat", Query: "hello"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if response.Text != "tenant answer" {
		t.Fatalf("expected the tenant provider answer, got %+v", response)
	}
	if gotModel != "tenant-chat" || gotAuth != "Bearer sk-test" {
		t.Fatalf("unexpected provider request: model=%q auth=%q", gotModel, gotAuth)
	}

	// A request without a tenant context cannot name a tenant provider; it must
	// stay on the deployment adapter instead of leaking a cross-tenant lookup.
	response, err = chatter.Complete(context.Background(), model.ChatRequest{Model: "tenant-chat", Query: "hello"})
	if err != nil || response.Text != "deployment" {
		t.Fatalf("expected deployment fallback without tenant, got %+v err=%v", response, err)
	}
}

func TestRoutingRerankerDispatchesAndSkipsOffline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.1}]}`)
	}))
	defer server.Close()

	routes := staticRoutes{"tenant-rerank": {
		ProviderID: "provider-2", Kind: modelregistry.ProviderKindOpenAICompatible,
		BaseURL: server.URL, Model: "tenant-rerank", Capability: model.CapabilityRerank, UpdatedAt: time.Now(),
	}}
	reranker := newRoutingReranker(retrieval.LexicalReranker{}, routes)
	if _, ok := reranker.(retrieval.ModelReranker); ok {
		t.Fatal("offline rerankers must not be wrapped; they ignore the resolved model")
	}

	remote, err := rerank.NewRemoteReranker(rerank.RemoteRerankerOptions{BaseURL: server.URL, Model: "deploy-rerank"})
	if err != nil {
		t.Fatalf("build deployment reranker: %v", err)
	}
	routed := newRoutingReranker(remote, routes)
	candidates := []retrieval.Candidate{{ChunkID: "a", Content: "first"}, {ChunkID: "b", Content: "second"}}
	ctx := tenantdb.WithTenant(context.Background(), "tenant-1")
	scored, err := routed.(retrieval.ModelReranker).RerankWithModel(ctx, "tenant-rerank", "query", candidates)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if scored[0].ChunkID != "a" || scored[0].RerankScore == nil {
		t.Fatalf("expected the provider to score the first candidate, got %+v", scored)
	}
}

func TestProbeProviderRouteUsesExplicitHealthURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer sk-test" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	err := probeProviderRoute(context.Background(), modelregistry.ProviderRoute{
		Kind: modelregistry.ProviderKindOpenAICompatible, BaseURL: server.URL,
		HealthURL: server.URL + "/health", APIKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	err = probeProviderRoute(context.Background(), modelregistry.ProviderRoute{
		Kind: modelregistry.ProviderKindOpenAICompatible, BaseURL: server.URL,
		HealthURL: server.URL + "/health", APIKey: "wrong",
	})
	if err == nil {
		t.Fatal("expected a credential rejection")
	}
}
