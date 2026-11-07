package conversation

import (
	"context"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

func TestGeneratePassesResolvedChatModelToAdapter(t *testing.T) {
	chatter := &recordingChatter{output: "[1] answer"}
	service := NewService(Options{Chat: chatter, Prompts: loadRewritePrompts(t)})
	evidence := retrieval.EvidenceSet{Items: []retrieval.Candidate{{
		ChunkID: "c1", DocumentID: "d1", DocumentVersionID: "v1", KnowledgeBaseID: "kb-a", Content: "alpha",
	}}}
	citations := NewRegistry(evidence)

	if _, err := service.generate(context.Background(), "question", citations, evidence, nil, "chat-b"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if chatter.request.Model != "chat-b" {
		t.Fatalf("adapter must receive the knowledge-base model, got %q", chatter.request.Model)
	}

	// An empty resolution means "keep the adapter's configured model", which is
	// what offline adapters rely on.
	if _, err := service.generate(context.Background(), "question", citations, evidence, nil, ""); err != nil {
		t.Fatalf("generate with default model: %v", err)
	}
	if chatter.request.Model != "" {
		t.Fatalf("unbound knowledge base must leave the model empty, got %q", chatter.request.Model)
	}
}
