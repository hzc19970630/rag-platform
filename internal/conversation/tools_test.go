package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/agenttools"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/prompts"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

type scriptedChatter struct {
	requests  []model.ChatRequest
	responses []model.ChatResponse
	supports  bool
}

func (c *scriptedChatter) Name() string { return "scripted" }
func (c *scriptedChatter) SupportsTools() bool {
	return c.supports
}

func (c *scriptedChatter) Complete(_ context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	c.requests = append(c.requests, request)
	index := len(c.requests) - 1
	if index >= len(c.responses) {
		return model.ChatResponse{}, nil
	}
	return c.responses[index], nil
}

type fakeRuntime struct {
	registry *agenttools.Registry
	enabled  bool
}

func (r *fakeRuntime) Enabled(context.Context, authz.AuthorizationScope, string) bool {
	return r.enabled
}

func (r *fakeRuntime) Registry() *agenttools.Registry { return r.registry }

func toolRegistry(t *testing.T) *agenttools.Registry {
	t.Helper()
	registry := agenttools.NewRegistry()
	err := registry.Register(agenttools.Tool{
		Spec: model.ToolSpec{
			Name: "table_query", Description: "query a table", ReadOnly: true,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"question": map[string]any{"type": "string"}},
				"required":   []any{"question"},
			},
		},
		Execute: func(context.Context, agenttools.Call) (agenttools.Result, error) {
			return agenttools.Result{
				Content: "sum=300",
				Evidence: []agenttools.Evidence{{
					Text: "金额合计 300", TableAssetID: "asset-1", Sheet: "表格 1", RowStart: 2, RowEnd: 3,
				}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}
	return registry
}

func TestGenerateWithToolsRegistersEvidenceAndEmitsEvents(t *testing.T) {
	promptRegistry, err := prompts.Load("../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	chatter := &scriptedChatter{
		supports: true,
		responses: []model.ChatResponse{
			{ToolCalls: []model.ToolCall{{ID: "call-1", Name: "table_query", Arguments: `{"question":"sum"}`}}},
			{Text: "合计是 300 [1]"},
		},
	}
	service := &Service{chat: chatter, tools: &fakeRuntime{registry: toolRegistry(t), enabled: true}, toolRounds: 3, toolCalls: 8, prompts: promptRegistry}
	events := []string{}
	response, evidence, err := service.generateWithTools(
		context.Background(), "金额合计", NewRegistry(retrieval.EvidenceSet{}), retrieval.EvidenceSet{},
		nil, "", authz.Principal{}, authz.AuthorizationScope{}, "kb-1",
		func(eventType string, _ map[string]any) error {
			events = append(events, eventType)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if response.Text != "合计是 300 [1]" {
		t.Fatalf("text = %q", response.Text)
	}
	if len(evidence.Items) != 1 || evidence.Items[0].TableAssetID != "asset-1" {
		t.Fatalf("evidence = %+v", evidence.Items)
	}
	if len(events) != 2 || events[0] != EventToolCall || events[1] != EventToolResult {
		t.Fatalf("events = %v", events)
	}
	if len(chatter.requests) != 2 {
		t.Fatalf("model requests = %d", len(chatter.requests))
	}
	second := chatter.requests[1]
	if len(second.Tools) != 1 || second.Evidence != nil {
		t.Fatalf("second request tools=%d evidence=%d", len(second.Tools), len(second.Evidence))
	}
	if len(second.History) != 2 || second.History[0].Role != "assistant" || second.History[1].Role != "tool" {
		t.Fatalf("tool history = %+v", second.History)
	}
	if second.History[1].ToolCallID != "call-1" {
		t.Fatalf("tool result call id = %q", second.History[1].ToolCallID)
	}
}

func TestGenerateWithToolsRetriesOnceWhenToolAnswerIsUncited(t *testing.T) {
	promptRegistry, err := prompts.Load("../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	chatter := &scriptedChatter{
		supports: true,
		responses: []model.ChatResponse{
			{ToolCalls: []model.ToolCall{{ID: "call-1", Name: "table_query", Arguments: `{"question":"sum"}`}}},
			{Text: "合计是 300"},
			{Text: "合计是 300 [1]"},
		},
	}
	service := &Service{chat: chatter, tools: &fakeRuntime{registry: toolRegistry(t), enabled: true}, toolRounds: 3, toolCalls: 8, prompts: promptRegistry}
	response, evidence, err := service.generateWithTools(
		context.Background(), "金额合计", NewRegistry(retrieval.EvidenceSet{}), retrieval.EvidenceSet{},
		nil, "", authz.Principal{}, authz.AuthorizationScope{}, "kb-1",
		func(string, map[string]any) error { return nil },
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if response.Text != "合计是 300 [1]" {
		t.Fatalf("text = %q", response.Text)
	}
	if len(evidence.Items) != 1 {
		t.Fatalf("evidence = %+v", evidence.Items)
	}
	if len(chatter.requests) != 3 {
		t.Fatalf("model requests = %d, want 3 (tool round, answer, citation retry)", len(chatter.requests))
	}
	retry := chatter.requests[2]
	if len(retry.Tools) != 0 {
		t.Fatalf("citation retry must not expose tools: %d", len(retry.Tools))
	}
	last := retry.History[len(retry.History)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "[1]") {
		t.Fatalf("citation retry history tail = %+v", last)
	}
}

type toolChoiceChatter struct {
	requests []model.ChatRequest
}

func (c *toolChoiceChatter) Name() string        { return "toolchoice" }
func (c *toolChoiceChatter) SupportsTools() bool { return true }

func (c *toolChoiceChatter) Complete(_ context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	c.requests = append(c.requests, request)
	switch len(c.requests) {
	case 1:
		if request.ToolChoice == "required" {
			return model.ChatResponse{}, errors.New("model provider returned 400: Thinking mode does not support this tool_choice")
		}
		return model.ChatResponse{}, nil
	case 2:
		return model.ChatResponse{ToolCalls: []model.ToolCall{{ID: "call-1", Name: "table_query", Arguments: `{"question":"sum"}`}}}, nil
	default:
		return model.ChatResponse{Text: "合计是 300 [1]"}, nil
	}
}

func TestGenerateWithToolsDegradesRequiredToolChoice(t *testing.T) {
	promptRegistry, err := prompts.Load("../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	chatter := &toolChoiceChatter{}
	service := &Service{chat: chatter, tools: &fakeRuntime{registry: toolRegistry(t), enabled: true}, toolRounds: 3, toolCalls: 8, prompts: promptRegistry}
	response, evidence, err := service.generateWithTools(
		context.Background(), "表格 1 的金额总和是多少", NewRegistry(retrieval.EvidenceSet{}), retrieval.EvidenceSet{},
		nil, "", authz.Principal{}, authz.AuthorizationScope{}, "kb-1",
		func(string, map[string]any) error { return nil },
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if response.Text != "合计是 300 [1]" || len(evidence.Items) != 1 {
		t.Fatalf("response=%q evidence=%d", response.Text, len(evidence.Items))
	}
	if len(chatter.requests) != 3 {
		t.Fatalf("model requests = %d, want 3 (required, auto retry, answer)", len(chatter.requests))
	}
	if chatter.requests[0].ToolChoice != "required" || chatter.requests[1].ToolChoice != "auto" {
		t.Fatalf("tool choice sequence = %q, %q", chatter.requests[0].ToolChoice, chatter.requests[1].ToolChoice)
	}
}

func TestToolsEnabledRequiresCapableModel(t *testing.T) {
	service := &Service{chat: &scriptedChatter{supports: false}, tools: &fakeRuntime{registry: toolRegistry(t), enabled: true}}
	if service.toolsEnabled(context.Background(), authz.AuthorizationScope{}, "kb-1", "") {
		t.Fatal("a model without tool support must disable the tool loop")
	}
	service.chat = &scriptedChatter{supports: true}
	if !service.toolsEnabled(context.Background(), authz.AuthorizationScope{}, "kb-1", "") {
		t.Fatal("capable model with enabled runtime should enable tools")
	}
}
