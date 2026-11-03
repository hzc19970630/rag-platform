// Package llm provides the generation adapters. The retrieval layer hands the
// model short citation ids, and the model can only reference ids it was given.
package llm

import (
	"context"
	"fmt"
	"strings"
)

// EvidenceItem is one authorized chunk presented to the model. Index is the
// short citation id the model must use when referring to it.
type EvidenceItem struct {
	Index       int
	ChunkID     string
	HeadingPath string
	Text        string
}

// ChatRequest is the generation input. ContextTemplate is injected from
// configuration, so no prompt wording lives in Go code.
type ChatRequest struct {
	SystemInstruction string
	ContextTemplate   string
	Query             string
	Evidence          []EvidenceItem
	MaxTokens         int
}

// ChatResponse is the model output plus the provenance recorded on the message.
type ChatResponse struct {
	Text             string
	Model            string
	PromptTokens     int
	CompletionTokens int
}

// Chatter is the generation port. Implementations must not create citations:
// they may only echo ids supplied in ChatRequest.Evidence.
type Chatter interface {
	Complete(ctx context.Context, request ChatRequest) (ChatResponse, error)
	Name() string
}

// RenderContext formats the evidence block list for a model prompt. Each block
// is delimited so document text cannot be confused with instructions.
func RenderContext(item EvidenceItem) string {
	heading := strings.TrimSpace(item.HeadingPath)
	if heading == "" {
		return fmt.Sprintf("[%d] %s", item.Index, item.Text)
	}
	return fmt.Sprintf("[%d] (%s) %s", item.Index, heading, item.Text)
}

// ApplyContextTemplate fills the injected context template.
func ApplyContextTemplate(template, query string, evidence []EvidenceItem) string {
	blocks := make([]string, 0, len(evidence))
	for _, item := range evidence {
		blocks = append(blocks, RenderContext(item))
	}
	rendered := strings.ReplaceAll(template, "{{evidence}}", strings.Join(blocks, "\n\n"))
	return strings.ReplaceAll(rendered, "{{query}}", query)
}
