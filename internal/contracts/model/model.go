// Package model holds the provider-neutral generation contract shared by the
// domain packages. The interfaces live with their consumers; only the request
// and response data shapes are shared so adapters and callers agree on the wire.
package model

import (
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

// Capability names one routable model role. Providers may serve more than one.
type Capability string

const (
	CapabilityChat      Capability = "chat"
	CapabilityEmbedding Capability = "embedding"
	CapabilityRerank    Capability = "rerank"
)

// ModelRef identifies the model and version a binding resolves to. The version
// is recorded in audit entries so an in-place model upgrade is distinguishable
// from a routing change.
type ModelRef struct {
	Model    string
	Provider string
	Version  string
}

// Turn is one prior conversation message. History is untrusted context: the
// remote adapter sends it as chat messages, never as instructions.
type Turn struct {
	Role    string
	Content string
}

// ChatRequest is the generation input. ContextTemplate is injected from
// configuration, so no prompt wording lives in Go code.
type ChatRequest struct {
	SystemInstruction string
	ContextTemplate   string
	Query             string
	Evidence          []EvidenceItem
	History           []Turn
	MaxTokens         int
	// Model overrides the adapter's configured model for this call. It is set
	// from the knowledge-base binding; empty keeps the deployment default.
	Model string
}

// ChatResponse is the model output plus the provenance recorded on the message.
type ChatResponse struct {
	Text             string
	Model            string
	PromptTokens     int
	CompletionTokens int
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
