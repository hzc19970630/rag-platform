package conversation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/llm"
	"github.com/hzc19970630/rag-platform/internal/prompts"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

type recordingChatter struct {
	request model.ChatRequest
	output  string
	err     error
}

func (r *recordingChatter) Complete(_ context.Context, request model.ChatRequest) (model.ChatResponse, error) {
	r.request = request
	return model.ChatResponse{Text: r.output}, r.err
}

func (r *recordingChatter) Name() string { return "remote-test" }

func loadRewritePrompts(t *testing.T) *prompts.Registry {
	t.Helper()
	registry, err := prompts.Load(filepath.Join("..", "..", "config", "prompt_templates"))
	if err != nil {
		t.Fatalf("load prompt templates: %v", err)
	}
	return registry
}

func TestQueryRewriterSendsHistoryAndReturnsStandaloneQuery(t *testing.T) {
	chatter := &recordingChatter{output: "second point details\n"}
	rewriter := NewQueryRewriter(chatter, loadRewritePrompts(t), time.Second, nil)
	history := []retrieval.HistoryTurn{
		{Role: MessageRoleUser, Content: "first question"},
		{Role: MessageRoleAssistant, Content: "first answer"},
	}
	got, err := rewriter.Rewrite(context.Background(), "and the second one?", history)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got != "second point details" {
		t.Fatalf("rewritten query = %q", got)
	}
	if len(chatter.request.History) != 2 {
		t.Fatalf("history turns = %d, want 2", len(chatter.request.History))
	}
	if chatter.request.History[0].Role != MessageRoleUser || chatter.request.History[1].Role != MessageRoleAssistant {
		t.Fatalf("history roles = %+v", chatter.request.History)
	}
	if chatter.request.MaxTokens != 128 {
		t.Fatalf("rewrite max tokens = %d", chatter.request.MaxTokens)
	}
}

func TestQueryRewriterFallsBackOnError(t *testing.T) {
	chatter := &recordingChatter{err: errors.New("provider down")}
	rewriter := NewQueryRewriter(chatter, loadRewritePrompts(t), time.Second, nil)
	got, err := rewriter.Rewrite(context.Background(), "original", []retrieval.HistoryTurn{{Role: MessageRoleUser, Content: "prior"}})
	if err == nil {
		t.Fatalf("expected the provider error")
	}
	if got != "original" {
		t.Fatalf("fallback query = %q", got)
	}
}

func TestQueryRewriterSkipsLocalAdapter(t *testing.T) {
	rewriter := NewQueryRewriter(llm.NewLocalChatter(), loadRewritePrompts(t), time.Second, nil)
	got, err := rewriter.Rewrite(context.Background(), "original", []retrieval.HistoryTurn{{Role: MessageRoleUser, Content: "prior"}})
	if err != nil || got != "original" {
		t.Fatalf("local rewrite = %q, %v", got, err)
	}
}

func TestCleanRewrite(t *testing.T) {
	cases := map[string]string{
		"  \"standalone\"\n": "standalone",
		"'second point'":     "second point",
		"one\nignore this":   "one",
		"  ":                 "",
	}
	for input, want := range cases {
		if got := cleanRewrite(input); got != want {
			t.Fatalf("cleanRewrite(%q) = %q, want %q", input, got, want)
		}
	}
}
