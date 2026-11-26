// Package agenttools is the single registry shared by the conversation tool
// loop and the MCP server. A tool is a read-only capability with one schema,
// one authorizer and one executor; callers never hard-code names or argument
// validation.
package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
)

const DefaultOutputLimit = 16 * 1024

// Scope is the authorization context a tool may inspect. It is deliberately
// small: a tool must not reach around the conversation's KB authorization.
type Scope struct {
	Principal       authz.Principal
	Authorization   authz.AuthorizationScope
	KnowledgeBaseID string
	TenantID        string
}

// Call is one validated invocation.
type Call struct {
	Scope     Scope
	Arguments json.RawMessage
	Parsed    map[string]any
}

// Result is what an executor returns. Content is the model-visible text and is
// always wrapped/truncated by the registry; Data is kept for citations, SSE
// events and MCP structured output.
type Result struct {
	Content  string
	Data     any
	Evidence []Evidence
}

// Evidence is one citable fact produced by a tool. The conversation assigns
// the citation index and registers it before the next model round.
type Evidence struct {
	Text              string
	ChunkID           string
	DocumentID        string
	DocumentVersionID string
	KnowledgeBaseID   string
	HeadingPath       string
	PageNumber        int
	SourcePath        string
	TableAssetID      string
	Sheet             string
	RowStart          int
	RowEnd            int
}

// Tool binds a provider-neutral spec to its authorization and execution.
type Tool struct {
	Spec        model.ToolSpec
	Version     string
	OutputLimit int
	Authorize   func(context.Context, Scope) error
	Execute     func(context.Context, Call) (Result, error)
}

// Outcome is the bounded, model-readable result of one invocation. IsError is
// true for recoverable failures (unknown tool, bad arguments, denied access,
// executor error); the loop feeds it back to the model instead of aborting.
type Outcome struct {
	ToolName  string
	Content   string
	Data      any
	Evidence  []Evidence
	IsError   bool
	Truncated bool
}

// Registry holds the first-party tool set.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register validates the spec and rejects duplicate names. The registry never
// loads plugins or user-defined tools.
func (r *Registry) Register(tool Tool) error {
	if err := model.ValidateToolSpec(tool.Spec); err != nil {
		return err
	}
	if tool.Execute == nil {
		return fmt.Errorf("tool %q has no executor", tool.Spec.Name)
	}
	if tool.Version == "" {
		tool.Version = "1"
	}
	if tool.OutputLimit <= 0 {
		tool.OutputLimit = DefaultOutputLimit
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[tool.Spec.Name]; exists {
		return fmt.Errorf("tool %q is registered twice", tool.Spec.Name)
	}
	r.tools[tool.Spec.Name] = tool
	return nil
}

// Get returns one tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Specs returns the stable sorted tool schemas both the model and MCP see.
func (r *Registry) Specs() []model.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]model.ToolSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, r.tools[name].Spec)
	}
	return specs
}

// Invoke validates and runs one call. Transport cancellation is reported as a
// recoverable error too; the caller still owns the loop timeout.
func (r *Registry) Invoke(ctx context.Context, name string, scope Scope, rawArgs string) Outcome {
	outcome := Outcome{ToolName: name}
	tool, ok := r.Get(name)
	if !ok {
		return failed(name, "unknown_tool", fmt.Sprintf("unknown tool %q", name))
	}
	if !tool.Spec.ReadOnly {
		return failed(name, "tool_not_read_only", fmt.Sprintf("tool %q is not read-only", name))
	}
	arguments := strings.TrimSpace(rawArgs)
	if arguments == "" {
		arguments = "{}"
	}
	if err := model.ValidateToolArguments(tool.Spec, arguments); err != nil {
		return failed(name, "invalid_arguments", err.Error())
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return failed(name, "invalid_arguments", err.Error())
	}
	if tool.Authorize != nil {
		if err := tool.Authorize(ctx, scope); err != nil {
			return failed(name, "tool_forbidden", err.Error())
		}
	}
	result, err := runExecutor(ctx, tool, Call{Scope: scope, Arguments: json.RawMessage(arguments), Parsed: parsed})
	if err != nil {
		return failed(name, "tool_failed", err.Error())
	}
	content := result.Content
	if strings.TrimSpace(content) == "" && result.Data != nil {
		if encoded, err := json.Marshal(result.Data); err == nil {
			content = string(encoded)
		}
	}
	content, truncated := truncate(content, tool.OutputLimit)
	outcome.Content = content
	outcome.Data = result.Data
	outcome.Evidence = result.Evidence
	outcome.Truncated = truncated
	return outcome
}

func runExecutor(ctx context.Context, tool Tool, call Call) (result Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{}
			err = fmt.Errorf("tool %q panicked: %v", tool.Spec.Name, recovered)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return tool.Execute(ctx, call)
}

func failed(name, code, message string) Outcome {
	payload, _ := json.Marshal(map[string]string{"error": code, "message": message})
	return Outcome{ToolName: name, Content: string(payload), IsError: true}
}

func truncate(content string, limit int) (string, bool) {
	if limit <= 0 || len(content) <= limit {
		return content, false
	}
	marker := "\n[tool output truncated]"
	if limit <= len(marker) {
		return content[:limit], true
	}
	return content[:limit-len(marker)] + marker, true
}
