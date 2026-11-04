package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/platform/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// RemoteChatter calls an OpenAI-compatible /chat/completions endpoint, which
// covers hosted providers and self-hosted gateways without provider SDKs.
type RemoteChatter struct {
	baseURL   string
	apiKey    string
	model     string
	maxTokens int
	client    *http.Client
}

type RemoteChatterOptions struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Timeout   time.Duration
}

func NewRemoteChatter(opts RemoteChatterOptions) (*RemoteChatter, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("chat base url is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("chat model is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 1024
	}
	return &RemoteChatter{
		baseURL:   strings.TrimRight(opts.BaseURL, "/"),
		apiKey:    opts.APIKey,
		model:     opts.Model,
		maxTokens: opts.MaxTokens,
		client:    &http.Client{Timeout: opts.Timeout},
	}, nil
}

func (c *RemoteChatter) Name() string { return c.model }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *RemoteChatter) Complete(ctx context.Context, request ChatRequest) (response ChatResponse, err error) {
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}
	ctx, span := observability.StartSpan(ctx, "llm.chat",
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", c.model),
		attribute.Int("gen_ai.request.max_tokens", maxTokens),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	messages := make([]chatMessage, 0, len(request.History)+2)
	messages = append(messages, chatMessage{Role: "system", Content: request.SystemInstruction})
	for _, turn := range request.History {
		messages = append(messages, chatMessage{Role: turn.Role, Content: turn.Content})
	}
	messages = append(messages, chatMessage{Role: "user", Content: ApplyContextTemplate(request.ContextTemplate, request.Query, request.Evidence)})
	body, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: 0,
	})
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpResponse, err := c.client.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call chat provider: %w", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, 16<<20))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read chat response: %w", err)
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("chat provider returned %d: %s", httpResponse.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded chatResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return ChatResponse{}, fmt.Errorf("chat provider error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat provider returned no choices")
	}
	model := decoded.Model
	if model == "" {
		model = c.model
	}
	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", decoded.Usage.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", decoded.Usage.CompletionTokens),
		attribute.String("gen_ai.response.model", model),
	)
	return ChatResponse{
		Text:             decoded.Choices[0].Message.Content,
		Model:            model,
		PromptTokens:     decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens,
	}, nil
}
