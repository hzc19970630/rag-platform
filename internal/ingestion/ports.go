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

// EmbeddingRecord is one chunk vector produced by the embed stage. The writer
// owns persistence so a vector-store adapter can replace PostgreSQL without
// changing the pipeline.
type EmbeddingRecord struct {
	ChunkID string
	Vector  []float32
}

// EvidenceIndexWriter is the write side of the Evidence Index. The PostgreSQL
// adapter stores vectors on the chunk row and lets BM25 follow the row write;
// an external adapter can index the same records in a search engine.
type EvidenceIndexWriter interface {
	// WriteEmbeddings persists chunk vectors. A non-empty versionID also marks
	// that DocumentVersion EMBEDDED; rebuild passes an empty versionID to update
	// vectors without changing the published status.
	WriteEmbeddings(ctx context.Context, versionID, model string, records []EmbeddingRecord) error
	// IndexVersion publishes a version whose chunks are embedded.
	IndexVersion(ctx context.Context, versionID string) error
	// DeleteVersion removes a version from the index. PostgreSQL is a no-op
	// because chunks live in the same table; external adapters must implement it.
	DeleteVersion(ctx context.Context, versionID string) error
}

type TaskPublisher interface {
	Publish(ctx context.Context, task Task) error
	// RemoveArchived drops the queue's terminal record of a task after an
	// operator has resolved its dead letter, so archived depth only reflects
	// failures that still need attention.
	RemoveArchived(ctx context.Context, task Task) error
}
