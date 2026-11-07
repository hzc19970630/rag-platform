package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/prompts"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

const queryRewriteMaxRunes = 400

// QueryRewriter turns a follow-up question into a standalone retrieval query.
// It is only used when history exists and a real generation adapter is wired;
// the deterministic local adapter would not produce a rewrite.
type QueryRewriter struct {
	chat    Chatter
	prompts *prompts.Registry
	timeout time.Duration
	metrics metrics.Recorder
}

func NewQueryRewriter(chat Chatter, registry *prompts.Registry, timeout time.Duration, recorder metrics.Recorder) *QueryRewriter {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	return &QueryRewriter{chat: chat, prompts: registry, timeout: timeout, metrics: recorder}
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
	turns := make([]model.Turn, len(history))
	for index, turn := range history {
		turns[index] = model.Turn{Role: turn.Role, Content: turn.Content}
	}
	rewriteCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := r.chat.Complete(rewriteCtx, model.ChatRequest{
		SystemInstruction: system,
		ContextTemplate:   template,
		Query:             query,
		History:           turns,
		MaxTokens:         128,
	})
	if err != nil {
		r.metrics.Inc("query_rewrite_total", metrics.Labels("result", "failure"))
		return query, err
	}
	rewritten := cleanRewrite(response.Text)
	if rewritten == "" || len([]rune(rewritten)) > queryRewriteMaxRunes {
		r.metrics.Inc("query_rewrite_total", metrics.Labels("result", "fallback"))
		return query, nil
	}
	r.metrics.Inc("query_rewrite_total", metrics.Labels("result", "success"))
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
