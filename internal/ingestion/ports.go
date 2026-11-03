package ingestion

import (
	"context"

	"github.com/example/rag-platform/internal/platform/embedding"
)

type ObjectStore interface {
	Put(ctx context.Context, key string, content []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type DocumentReader interface {
	Parse(ctx context.Context, source SourceObject, content []byte) (ParseResult, error)
}

// Embedder is the shared model port; the pipeline depends on the interface only.
type Embedder = embedding.Embedder

type KeywordIndexer interface {
	Index(ctx context.Context, documentVersionID string) error
}

type TaskPublisher interface {
	Publish(ctx context.Context, task Task) error
	// RemoveArchived drops the queue's terminal record of a task after an
	// operator has resolved its dead letter, so archived depth only reflects
	// failures that still need attention.
	RemoveArchived(ctx context.Context, task Task) error
}
