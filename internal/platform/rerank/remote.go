package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/contracts/ports"
	"github.com/example/rag-platform/internal/platform/netprobe"
)

// RemoteReranker calls an OpenAI compatible /rerank endpoint. The retrieval
// layer depends only on ports.Reranker, so swapping the offline overlap
// reranker for a hosted model is a configuration change, not a code change.
//
// The adapter only annotates scores: the fused score stays on the candidate
// because the citation layer reports it, and rerank_score carries the model's
// own relevance so rerank_threshold is comparable across adapters.
type RemoteReranker struct {
	baseURL   string
	apiKey    string
	model     string
	healthURL string
	client    *http.Client
}

type RemoteRerankerOptions struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	// HealthURL overrides the liveness endpoint. Empty uses /health, the
	// conventional endpoint for the self-hosted rerank servers this targets.
	HealthURL string
}

func NewRemoteReranker(options RemoteRerankerOptions) (*RemoteReranker, error) {
	if strings.TrimSpace(options.BaseURL) == "" {
		return nil, fmt.Errorf("rerank base URL is required")
	}
	if strings.TrimSpace(options.Model) == "" {
		return nil, fmt.Errorf("rerank model is required")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	healthURL := strings.TrimSpace(options.HealthURL)
	if healthURL == "" {
		healthURL = baseURL + "/health"
	}
	return &RemoteReranker{
		baseURL:   baseURL,
		apiKey:    strings.TrimSpace(options.APIKey),
		model:     options.Model,
		healthURL: healthURL,
		client:    &http.Client{Timeout: timeout},
	}, nil
}

// Name is the configured model, matching the chat and embedding adapters so the
// model catalog registers names a knowledge-base binding can actually select.
func (r *RemoteReranker) Name() string { return r.model }

// Probe reports provider reachability for the model catalog.
func (r *RemoteReranker) Probe(ctx context.Context) error {
	return netprobe.Check(ctx, r.healthURL, r.apiKey)
}

type rerankRequest struct {
	Model     string           `json:"model"`
	Query     string           `json:"query"`
	Documents []rerankDocument `json:"documents"`
}

type rerankDocument struct {
	Text string `json:"text"`
}

type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (r *RemoteReranker) Rerank(ctx context.Context, query string, candidates []ports.Candidate) ([]ports.Candidate, error) {
	return r.RerankWithModel(ctx, r.model, query, candidates)
}

// RerankWithModel reranks with the resolved knowledge-base model. An empty
// model falls back to the adapter's configured default.
func (r *RemoteReranker) RerankWithModel(ctx context.Context, model, query string, candidates []ports.Candidate) ([]ports.Candidate, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	if strings.TrimSpace(model) == "" {
		model = r.model
	}
	documents := make([]rerankDocument, 0, len(candidates))
	for _, candidate := range candidates {
		documents = append(documents, rerankDocument{Text: candidate.Content})
	}
	payload, err := json.Marshal(rerankRequest{Model: model, Query: query, Documents: documents})
	if err != nil {
		return nil, fmt.Errorf("encode rerank request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/rerank", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build rerank request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read rerank response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank endpoint returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed rerankResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}

	scored := make([]ports.Candidate, len(candidates))
	copy(scored, candidates)
	annotated := 0
	for _, result := range parsed.Results {
		// An index outside the candidate set would mean the provider answered a
		// different request; refusing it keeps the score attached to the text it
		// was computed for.
		if result.Index < 0 || result.Index >= len(scored) {
			return nil, fmt.Errorf("rerank endpoint returned index %d for %d candidates", result.Index, len(scored))
		}
		value := result.RelevanceScore
		scored[result.Index].RerankScore = &value
		if !contains(scored[result.Index].Channels, ports.ChannelReranked) {
			scored[result.Index].Channels = append(scored[result.Index].Channels, ports.ChannelReranked)
		}
		annotated++
	}
	if annotated == 0 {
		return nil, fmt.Errorf("rerank endpoint returned no results for %d candidates", len(scored))
	}

	ports.SortCandidates(scored)
	return scored, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
