package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/platform/llm"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/example/rag-platform/internal/retrieval"
)

const queryRewriteMaxRunes = 400

// QueryRewriter turns a follow-up question into a standalone retrieval query.
// It is only used when history exists and a real generation adapter is wired;
// the deterministic local adapter would not produce a rewrite.
type QueryRewriter struct {
	chat    llm.Chatter
	prompts *prompts.Registry
	timeout time.Duration
}

func NewQueryRewriter(chat llm.Chatter, registry *prompts.Registry, timeout time.Duration) *QueryRewriter {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &QueryRewriter{chat: chat, prompts: registry, timeout: timeout}
}

func (r *QueryRewriter) Rewrite(ctx context.Context, query string, history []retrieval.HistoryTurn) (string, error) {
	if r == nil || r.chat == nil || r.prompts == nil || len(history) == 0 || r.chat.Name() == localChatName {
		return query, nil
	}
	system, err := r.prompts.Render(prompts.QueryRewriteSystem, nil)
	if err != nil {
		return query, err
	}
	template, ok := r.prompts.Raw(prompts.QueryRewrite)
	if !ok {
		return query, fmt.Errorf("query rewrite template is not configured")
	}
	turns := make([]llm.Turn, len(history))
	for index, turn := range history {
		turns[index] = llm.Turn{Role: turn.Role, Content: turn.Content}
	}
	rewriteCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := r.chat.Complete(rewriteCtx, llm.ChatRequest{
		SystemInstruction: system,
		ContextTemplate:   template,
		Query:             query,
		History:           turns,
		MaxTokens:         128,
	})
	if err != nil {
		observability.Default().Inc("query_rewrite_total", observability.Labels("result", "failure"))
		return query, err
	}
	rewritten := cleanRewrite(response.Text)
	if rewritten == "" || len([]rune(rewritten)) > queryRewriteMaxRunes {
		observability.Default().Inc("query_rewrite_total", observability.Labels("result", "fallback"))
		return query, nil
	}
	observability.Default().Inc("query_rewrite_total", observability.Labels("result", "success"))
	return rewritten, nil
}

// cleanRewrite keeps the rewriter's output to one query line and removes the
// quoting models commonly add around a single value.
func cleanRewrite(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	value = strings.Trim(value, " \t\"'`")
	return strings.Join(strings.Fields(value), " ")
}
