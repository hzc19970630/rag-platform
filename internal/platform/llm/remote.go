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

	"github.com/hzc19970630/rag-platform/internal/platform/netprobe"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/resilience"
)

// RemoteChatter calls an OpenAI-compatible /chat/completions endpoint, which
// covers hosted providers and self-hosted gateways without provider SDKs.
type RemoteChatter struct {
	baseURL       string
	apiKey        string
	model         string
	maxTokens     int
	healthURL     string
	supportsTools bool
	client        *http.Client
	guard         *resilience.Guard
}

type RemoteChatterOptions struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Timeout   time.Duration
	// HealthURL overrides the liveness endpoint. Empty uses /models, which is
	// part of the OpenAI-compatible surface this adapter targets.
	HealthURL string
	// Guard is optional; nil creates a default breaker/bulkhead/retry budget.
	Guard *resilience.Guard
	// SupportsTools declares the model can accept function schemas. Nil means
	// true for OpenAI-compatible endpoints; a model entry may opt out.
	SupportsTools *bool
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
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	healthURL := strings.TrimSpace(opts.HealthURL)
	if healthURL == "" {
		healthURL = baseURL + "/models"
	}
	guard := opts.Guard
	if guard == nil {
		guard = resilience.New(resilience.DefaultConfig())
	}
	supportsTools := true
	if opts.SupportsTools != nil {
		supportsTools = *opts.SupportsTools
	}
	return &RemoteChatter{
		baseURL:       baseURL,
		apiKey:        opts.APIKey,
		model:         opts.Model,
		maxTokens:     opts.MaxTokens,
		healthURL:     healthURL,
		supportsTools: supportsTools,
		client:        &http.Client{Timeout: opts.Timeout},
		guard:         guard,
	}, nil
}

func (c *RemoteChatter) Name() string { return c.model }

// SupportsTools reports whether this model accepts function schemas.
func (c *RemoteChatter) SupportsTools() bool { return c.supportsTools }

// SupportsToolCalls implements the per-model resolver for routing adapters.
func (c *RemoteChatter) SupportsToolCalls(context.Context, string) bool { return c.supportsTools }

// Probe reports provider reachability for the model catalog. The catalog caches
// the result, so this runs at most once per health TTL.
func (c *RemoteChatter) Probe(ctx context.Context) error {
	return netprobe.Check(ctx, c.healthURL, c.apiKey)
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatMessage struct {
	Role         string                `json:"role"`
	Content      string                `json:"content,omitempty"`
	ToolCalls    []chatToolCall        `json:"tool_calls,omitempty"`
	ToolCallID   string                `json:"tool_call_id,omitempty"`
	Name         string                `json:"name,omitempty"`
	FunctionCall *chatToolCallFunction `json:"function_call,omitempty"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []chatTool    `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *RemoteChatter) Complete(ctx context.Context, request model.ChatRequest) (response model.ChatResponse, err error) {
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}
	modelName := strings.TrimSpace(request.Model)
	if modelName == "" {
		modelName = c.model
	}
	ctx, span := observability.StartSpan(ctx, "llm.chat",
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", modelName),
		attribute.Int("gen_ai.request.max_tokens", maxTokens),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	if len(request.Tools) > 0 && !c.supportsTools {
		return model.ChatResponse{}, fmt.Errorf("%w: model %s", model.ErrToolsUnsupported, modelName)
	}
	messages := make([]chatMessage, 0, len(request.History)+2)
	messages = append(messages, chatMessage{Role: "system", Content: request.SystemInstruction})
	for _, turn := range request.History {
		messages = append(messages, turnMessage(turn))
	}
	messages = append(messages, chatMessage{Role: "user", Content: model.ApplyContextTemplate(request.ContextTemplate, request.Query, request.Evidence)})
	tools := make([]chatTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		tools = append(tools, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name: spec.Name, Description: spec.Description, Parameters: spec.InputSchema,
			},
		})
	}
	toolChoice := request.ToolChoice
	if toolChoice == "" && len(tools) > 0 {
		toolChoice = "auto"
	}
	body, err := json.Marshal(chatRequest{
		Model:       modelName,
		Messages:    messages,
		Tools:       tools,
		ToolChoice:  toolChoice,
		MaxTokens:   maxTokens,
		Temperature: 0,
	})
	if err != nil {
		return model.ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}
	if err := c.guard.Do(ctx, func(attemptCtx context.Context) error {
		httpRequest, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build chat request: %w", err)
		}
		httpRequest.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		httpResponse, err := c.client.Do(httpRequest)
		if err != nil {
			if attemptCtx.Err() != nil {
				return attemptCtx.Err()
			}
			return resilience.Retryable(fmt.Errorf("call chat provider: %w", err))
		}
		defer httpResponse.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, 16<<20))
		if err != nil {
			return fmt.Errorf("read chat response: %w", err)
		}
		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			return resilience.ProviderStatusError(httpResponse.StatusCode, strings.TrimSpace(string(raw)))
		}
		var decoded chatResponse
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return fmt.Errorf("decode chat response: %w", err)
		}
		if decoded.Error != nil && decoded.Error.Message != "" {
			return fmt.Errorf("chat provider error: %s", decoded.Error.Message)
		}
		if len(decoded.Choices) == 0 {
			return fmt.Errorf("chat provider returned no choices")
		}
		responseModel := decoded.Model
		if responseModel == "" {
			responseModel = modelName
		}
		choice := decoded.Choices[0]
		calls := make([]model.ToolCall, 0, len(choice.Message.ToolCalls)+1)
		for index, call := range choice.Message.ToolCalls {
			if call.Type != "" && call.Type != "function" {
				continue
			}
			id := call.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", index)
			}
			calls = append(calls, model.ToolCall{ID: id, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		if legacy := choice.Message.FunctionCall; legacy != nil && legacy.Name != "" {
			calls = append(calls, model.ToolCall{ID: "call_legacy", Name: legacy.Name, Arguments: legacy.Arguments})
		}
		response = model.ChatResponse{
			Text:             choice.Message.Content,
			ToolCalls:        calls,
			Model:            responseModel,
			PromptTokens:     decoded.Usage.PromptTokens,
			CompletionTokens: decoded.Usage.CompletionTokens,
		}
		span.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", decoded.Usage.PromptTokens),
			attribute.Int("gen_ai.usage.output_tokens", decoded.Usage.CompletionTokens),
			attribute.String("gen_ai.response.model", responseModel),
		)
		return nil
	}); err != nil {
		return model.ChatResponse{}, err
	}
	return response, nil
}

func turnMessage(turn model.Turn) chatMessage {
	message := chatMessage{Role: turn.Role, Content: turn.Content}
	if turn.Role == "tool" {
		message.ToolCallID = turn.ToolCallID
		message.Name = turn.Name
		return message
	}
	for _, call := range turn.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, chatToolCall{
			ID: call.ID, Type: "function",
			Function: chatToolCallFunction{Name: call.Name, Arguments: call.Arguments},
		})
	}
	return message
}
