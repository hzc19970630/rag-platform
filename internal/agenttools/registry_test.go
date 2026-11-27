package agenttools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
)

func spec(name string) model.ToolSpec {
	return model.ToolSpec{
		Name: name, Description: name, ReadOnly: true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
			},
			"required": []any{"question"},
		},
	}
}

func echoTool(name string) Tool {
	return Tool{
		Spec: spec(name),
		Execute: func(ctx context.Context, call Call) (Result, error) {
			return Result{Content: "answer:" + call.Parsed["question"].(string)}, nil
		},
	}
}

func TestRegistryRegisterAndSpecs(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(echoTool("b_tool")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register(echoTool("a_tool")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register(echoTool("a_tool")); err == nil {
		t.Fatal("duplicate tool accepted")
	}
	if err := registry.Register(Tool{Spec: spec("no_exec")}); err == nil {
		t.Fatal("tool without executor accepted")
	}
	specs := registry.Specs()
	if len(specs) != 2 || specs[0].Name != "a_tool" || specs[1].Name != "b_tool" {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestRegistryInvoke(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(echoTool("echo")); err != nil {
		t.Fatalf("register: %v", err)
	}
	outcome := registry.Invoke(context.Background(), "echo", Scope{}, `{"question":"hi"}`)
	if outcome.IsError || outcome.Content != "answer:hi" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome := registry.Invoke(context.Background(), "missing", Scope{}, `{}`); !outcome.IsError || !strings.Contains(outcome.Content, "unknown_tool") {
		t.Fatalf("unknown outcome = %+v", outcome)
	}
	if outcome := registry.Invoke(context.Background(), "echo", Scope{}, `{}`); !outcome.IsError || !strings.Contains(outcome.Content, "invalid_arguments") {
		t.Fatalf("argument outcome = %+v", outcome)
	}
}

func TestRegistryAuthorizationReadOnlyAndRecovery(t *testing.T) {
	registry := NewRegistry()
	denied := echoTool("denied")
	denied.Guard = func(context.Context, Scope) error { return errors.New("no access") }
	if err := registry.Register(denied); err != nil {
		t.Fatalf("register: %v", err)
	}
	if outcome := registry.Invoke(context.Background(), "denied", Scope{}, `{"question":"x"}`); !outcome.IsError || !strings.Contains(outcome.Content, "tool_forbidden") {
		t.Fatalf("denied outcome = %+v", outcome)
	}
	write := echoTool("write")
	write.Spec.ReadOnly = false
	if err := registry.Register(write); err != nil {
		t.Fatalf("register: %v", err)
	}
	if outcome := registry.Invoke(context.Background(), "write", Scope{}, `{"question":"x"}`); !outcome.IsError || !strings.Contains(outcome.Content, "tool_not_read_only") {
		t.Fatalf("write outcome = %+v", outcome)
	}
	crash := echoTool("crash")
	crash.Execute = func(context.Context, Call) (Result, error) { panic("boom") }
	if err := registry.Register(crash); err != nil {
		t.Fatalf("register: %v", err)
	}
	if outcome := registry.Invoke(context.Background(), "crash", Scope{}, `{"question":"x"}`); !outcome.IsError || !strings.Contains(outcome.Content, "panicked") {
		t.Fatalf("panic outcome = %+v", outcome)
	}
}

func TestRegistryTruncatesOutputAndMarshalsData(t *testing.T) {
	registry := NewRegistry()
	long := echoTool("long")
	long.OutputLimit = 32
	long.Execute = func(context.Context, Call) (Result, error) { return Result{Content: strings.Repeat("x", 100)}, nil }
	if err := registry.Register(long); err != nil {
		t.Fatalf("register: %v", err)
	}
	outcome := registry.Invoke(context.Background(), "long", Scope{}, `{"question":"x"}`)
	if !outcome.Truncated || len(outcome.Content) != 32 || !strings.Contains(outcome.Content, "[tool output truncated]") {
		t.Fatalf("truncated outcome = %q (%d)", outcome.Content, len(outcome.Content))
	}
	data := echoTool("data")
	data.Execute = func(context.Context, Call) (Result, error) { return Result{Data: map[string]any{"rows": 2}}, nil }
	if err := registry.Register(data); err != nil {
		t.Fatalf("register: %v", err)
	}
	if outcome := registry.Invoke(context.Background(), "data", Scope{}, `{"question":"x"}`); outcome.Content != `{"rows":2}` {
		t.Fatalf("data content = %q", outcome.Content)
	}
}
