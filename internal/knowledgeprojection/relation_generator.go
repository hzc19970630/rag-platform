package knowledgeprojection

import (
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection/relation"
	"github.com/hzc19970630/rag-platform/internal/prompts"
)

// Extractor lives in the relation subpackage; the alias keeps the shared
// orchestration on the existing name.
type Extractor = relation.Extractor

func NewExtractor(chatter Chatter, registry *prompts.Registry) (*Extractor, error) {
	return relation.NewExtractor(chatter, registry)
}
