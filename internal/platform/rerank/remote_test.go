package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/retrieval"
)

func TestRemoteRerankerUsesTheResolvedModel(t *testing.T) {
	var received rerankRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"index": 0, "relevance_score": 0.9}},
		})
	}))
	defer server.Close()

	reranker, err := NewRemoteReranker(RemoteRerankerOptions{
		BaseURL: server.URL, Model: "deployment-rerank", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("build reranker: %v", err)
	}
	if reranker.Name() != "deployment-rerank" {
		t.Fatalf("Name must be the configured model so the catalog can register it, got %q", reranker.Name())
	}
	scored, err := reranker.RerankWithModel(context.Background(), "kb-rerank", "question", []retrieval.Candidate{{ChunkID: "chunk-1", Content: "text"}})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if received.Model != "kb-rerank" {
		t.Fatalf("the provider must receive the resolved model, got %q", received.Model)
	}
	if len(scored) != 1 || scored[0].RerankScore == nil || *scored[0].RerankScore != 0.9 {
		t.Fatalf("unexpected scored candidates: %+v", scored)
	}
}

func TestRemoteRerankerProbeDistinguishesReachability(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer healthy.Close()
	reranker, err := NewRemoteReranker(RemoteRerankerOptions{BaseURL: healthy.URL, Model: "m"})
	if err != nil {
		t.Fatalf("build reranker: %v", err)
	}
	// A provider that answers 404 is reachable; probes assert liveness, not the
	// existence of a vendor-specific health route.
	if err := reranker.Probe(context.Background()); err != nil {
		t.Fatalf("a reachable provider must probe healthy: %v", err)
	}

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()
	failing, err := NewRemoteReranker(RemoteRerankerOptions{BaseURL: unhealthy.URL, Model: "m"})
	if err != nil {
		t.Fatalf("build reranker: %v", err)
	}
	if err := failing.Probe(context.Background()); err == nil {
		t.Fatalf("a 5xx provider must probe unhealthy")
	}
}
