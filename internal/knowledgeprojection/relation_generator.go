package knowledgeprojection

import (
	"github.com/example/rag-platform/internal/knowledgeprojection/relation"
	"github.com/example/rag-platform/internal/platform/llm"
	"github.com/example/rag-platform/internal/prompts"
)

// Extractor lives in the relation subpackage; the alias keeps the shared
// orchestration on the existing name.
type Extractor = relation.Extractor

func NewExtractor(chatter llm.Chatter, registry *prompts.Registry) (*Extractor, error) {
	return relation.NewExtractor(chatter, registry)
}
