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

func TestRemoteEmbedderBatchesByTokenBudget(t *testing.T) {
	var batchSizes []int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		batchSizes = append(batchSizes, len(payload.Input))
		data := make([]map[string]any, 0, len(payload.Input))
		for index := range payload.Input {
			data = append(data, map[string]any{"index": index, "embedding": []float32{0.25, 0.75}})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	embedder, err := NewRemoteEmbedder(RemoteEmbedderOptions{
		BaseURL: server.URL, Model: "test-model", Dimension: 2, BatchSize: 8,
		BatchMaxTokens: 4, CharsPerToken: 2, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRemoteEmbedder: %v", err)
	}
	if _, err := embedder.Embed(context.Background(), []string{"aaaa", "bbbb", "cccc", "dddd"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(batchSizes) != 2 || batchSizes[0] != 2 || batchSizes[1] != 2 {
		t.Fatalf("batch sizes = %v, want two batches of 2", batchSizes)
	}
}

func TestRemoteEmbedderHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.Header().Set("Retry-After", "1")
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeEmbeddingResponse(t, writer)
	}))
	defer server.Close()
	embedder, err := NewRemoteEmbedder(RemoteEmbedderOptions{
		BaseURL: server.URL, Model: "test-model", Dimension: 2, BatchSize: 8, Timeout: 2 * time.Second,
		RetryAfterMax: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRemoteEmbedder: %v", err)
	}
	started := time.Now()
	if _, err := embedder.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After not honored, elapsed=%s", elapsed)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
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
