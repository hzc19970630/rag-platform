package ingestion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestRemoteEmbedder(t *testing.T, handler func(writer http.ResponseWriter, request *http.Request, attempt int)) (*RemoteEmbedder, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler(writer, request, int(attempts.Add(1)))
	}))
	t.Cleanup(server.Close)
	embedder, err := NewRemoteEmbedder(RemoteEmbedderOptions{
		BaseURL: server.URL, Model: "test-model", Dimension: 2, BatchSize: 8, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRemoteEmbedder: %v", err)
	}
	previous := embedRetryBaseDelay
	embedRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { embedRetryBaseDelay = previous })
	return embedder, &attempts
}

func writeEmbeddingResponse(t *testing.T, writer http.ResponseWriter) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"data": []map[string]any{{"index": 0, "embedding": []float32{0.25, 0.75}}},
	}); err != nil {
		t.Errorf("encode embedding response: %v", err)
	}
}

func TestRemoteEmbedderRetriesTransientProviderFailures(t *testing.T) {
	embedder, attempts := newTestRemoteEmbedder(t, func(writer http.ResponseWriter, _ *http.Request, attempt int) {
		if attempt < 3 {
			http.Error(writer, "temporary", http.StatusInternalServerError)
			return
		}
		writeEmbeddingResponse(t, writer)
	})
	vectors, err := embedder.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 2 || vectors[0][0] != 0.25 {
		t.Fatalf("vectors = %#v", vectors)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("provider attempts = %d, want 3", got)
	}
}

func TestRemoteEmbedderDoesNotRetryClientErrors(t *testing.T) {
	embedder, attempts := newTestRemoteEmbedder(t, func(writer http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(writer, "bad request", http.StatusBadRequest)
	})
	if _, err := embedder.Embed(context.Background(), []string{"hello"}); err == nil {
		t.Fatal("expected a provider error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("provider attempts = %d, want 1", got)
	}
}

func TestRemoteEmbedderStopsOnCanceledContext(t *testing.T) {
	embedder, attempts := newTestRemoteEmbedder(t, func(writer http.ResponseWriter, _ *http.Request, _ int) {
		writeEmbeddingResponse(t, writer)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := embedder.Embed(ctx, []string{"hello"}); err == nil {
		t.Fatal("expected the canceled context error")
	}
	if got := attempts.Load(); got != 0 {
		t.Fatalf("provider attempts = %d, want 0", got)
	}
}
