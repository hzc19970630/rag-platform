package ingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/platform/netprobe"
)

// LocalEmbedder provides a deterministic, offline embedding adapter for local
// development and acceptance runs. It keeps the pipeline testable without
// pretending that a production model is configured. It is not a semantic
// model: the dimension is configurable only so the contract stays honest.
type LocalEmbedder struct {
	dimension int
}

func NewLocalEmbedder(dimension int) LocalEmbedder {
	if dimension <= 0 {
		dimension = 3
	}
	return LocalEmbedder{dimension: dimension}
}

func (e LocalEmbedder) Dimension() int { return e.dimension }

func (e LocalEmbedder) Name() string { return "local-deterministic" }

func (e LocalEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	result := make([][]float32, 0, len(texts))
	for _, text := range texts {
		values := make([]float32, e.dimension)
		for i := range values {
			// Re-hash per component so any configured dimension yields distinct
			// values instead of cycling through eight hash bytes.
			hash := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", text, i)))
			raw := binary.BigEndian.Uint32(hash[0:4])
			values[i] = float32(raw)/float32(math.MaxUint32)*2 - 1
		}
		result = append(result, values)
	}
	return result, nil
}

// RemoteEmbedder calls an OpenAI-compatible /embeddings endpoint. It covers
// most hosted providers and self-hosted gateways (vLLM, one-api style proxies)
// without provider-specific SDKs.
type RemoteEmbedder struct {
	baseURL   string
	apiKey    string
	model     string
	dimension int
	batchSize int
	healthURL string
	client    *http.Client
}

type RemoteEmbedderOptions struct {
	BaseURL   string
	APIKey    string
	Model     string
	Dimension int
	BatchSize int
	Timeout   time.Duration
	// HealthURL overrides the liveness endpoint. Empty uses /models, which is
	// part of the OpenAI-compatible surface this adapter targets.
	HealthURL string
}

func NewRemoteEmbedder(opts RemoteEmbedderOptions) (*RemoteEmbedder, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("embedding base url is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("embedding model is required")
	}
	if opts.Dimension <= 0 {
		return nil, fmt.Errorf("embedding dimension must be positive")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 32
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	healthURL := strings.TrimSpace(opts.HealthURL)
	if healthURL == "" {
		healthURL = baseURL + "/models"
	}
	return &RemoteEmbedder{
		baseURL:   baseURL,
		apiKey:    opts.APIKey,
		model:     opts.Model,
		dimension: opts.Dimension,
		batchSize: opts.BatchSize,
		healthURL: healthURL,
		client:    &http.Client{Timeout: opts.Timeout},
	}, nil
}

func (e *RemoteEmbedder) Dimension() int { return e.dimension }

func (e *RemoteEmbedder) Name() string { return e.model }

// Probe reports provider reachability for the model catalog.
func (e *RemoteEmbedder) Probe(ctx context.Context) error {
	return netprobe.Check(ctx, e.healthURL, e.apiKey)
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (e *RemoteEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := e.embedBatchWithRetry(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
	}
	return result, nil
}

const embedMaxAttempts = 3

const embedRetryMaxDelay = 2 * time.Second

// embedRetryBaseDelay is a variable so tests can shorten the backoff without
// sleeping through the production schedule.
var embedRetryBaseDelay = 250 * time.Millisecond

// embeddingTransportError marks a failure to reach the provider, which is worth
// retrying. Deterministic failures (bad status, malformed body, dimension
// mismatch) are left unmarked and fail after one attempt.
type embeddingTransportError struct {
	err error
}

func (e *embeddingTransportError) Error() string { return "call embedding provider: " + e.err.Error() }

func (e *embeddingTransportError) Unwrap() error { return e.err }

// embedStatusError carries the provider status so retries can distinguish a
// transient 429/5xx from a deterministic 4xx.
type embedStatusError struct {
	Status int
	Body   string
}

func (e *embedStatusError) Error() string {
	return fmt.Sprintf("embedding provider returned %d: %s", e.Status, e.Body)
}

func (e *RemoteEmbedder) embedBatchWithRetry(ctx context.Context, batch []string) ([][]float32, error) {
	var lastErr error
	for attempt := 1; attempt <= embedMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors, err := e.embedBatch(ctx, batch)
		if err == nil {
			return vectors, nil
		}
		lastErr = err
		if attempt == embedMaxAttempts || !retryableEmbedError(err) {
			break
		}
		delay := embedRetryBaseDelay << (attempt - 1)
		if delay > embedRetryMaxDelay {
			delay = embedRetryMaxDelay
		}
		slog.Warn("embedding_batch_retry", "attempt", attempt, "delay_ms", delay.Milliseconds(), "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func retryableEmbedError(err error) bool {
	var transport *embeddingTransportError
	if errors.As(err, &transport) {
		return true
	}
	var status *embedStatusError
	if errors.As(err, &status) {
		return status.Status == http.StatusTooManyRequests || status.Status >= 500
	}
	return false
}

func (e *RemoteEmbedder) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{Model: e.model, Input: batch})
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embedding request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	response, err := e.client.Do(request)
	if err != nil {
		return nil, &embeddingTransportError{err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read embedding response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &embedStatusError{Status: response.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	var decoded embeddingResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return nil, fmt.Errorf("embedding provider error: %s", decoded.Error.Message)
	}
	if len(decoded.Data) != len(batch) {
		return nil, fmt.Errorf("embedding provider returned %d vectors for %d inputs", len(decoded.Data), len(batch))
	}

	vectors := make([][]float32, len(batch))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("embedding provider returned out-of-range index %d", item.Index)
		}
		if len(item.Embedding) != e.dimension {
			return nil, fmt.Errorf("embedding dimension mismatch: configured %d, provider returned %d", e.dimension, len(item.Embedding))
		}
		vectors[item.Index] = item.Embedding
	}
	return vectors, nil
}
