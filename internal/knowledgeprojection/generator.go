package knowledgeprojection

import (
	"github.com/example/rag-platform/internal/knowledgeprojection/wiki"
	"github.com/example/rag-platform/internal/prompts"
)

// Generator and GeneratedPage live in the wiki subpackage. Aliases keep the
// shared orchestration and existing tests on the old names.
type Generator = wiki.Generator
type GeneratedPage = wiki.GeneratedPage

func NewGenerator(chatter Chatter, registry *prompts.Registry) (*Generator, error) {
	return wiki.NewGenerator(chatter, registry)
}
