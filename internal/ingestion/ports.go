package ingestion

import "context"

type ObjectStore interface {
	Put(ctx context.Context, key string, content []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type DocumentReader interface {
	Parse(ctx context.Context, source SourceObject, content []byte) (ParseResult, error)
}

// Embedder is the shared model port; the pipeline depends on the interface only.
// Embedder is the model port owned by this package. Adapters live here or in
// platform; the pipeline depends on the interface only.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension reports the vector length this adapter produces. Callers
	// validate it against stored rows so a model or dimension change fails
	// loudly instead of writing vectors that cannot be searched.
	Dimension() int
	// Name identifies the model so a knowledge base bound to a different one
	// cannot be written or queried with an incomparable vector.
	Name() string
}

// EmbeddingRecord is one chunk vector produced by the embed stage. The writer
// owns persistence so a vector-store adapter can replace PostgreSQL without
// changing the pipeline.
type EmbeddingRecord struct {
	ChunkID string
	Vector  []float32
}

// ChunkDocument is one searchable document as written by the chunk stage.
// Chunk rows themselves remain owned by the Evidence Store; this record is the
// hand-off to a keyword or hybrid index adapter.
type ChunkDocument struct {
	ID          string
	ParentID    string
	Ordinal     int
	Content     string
	ContentHash string
	HeadingPath string
	PageNumber  int
	SourcePath  string
	StartOffset int
	EndOffset   int
	Status      string
}

// EvidenceIndexWriter is the write side of the Evidence Index. The PostgreSQL
// adapter stores vectors on the chunk row and lets BM25 follow the row write;
// an external adapter can index the same records in a search engine.
type EvidenceIndexWriter interface {
	// IndexChunks mirrors chunk documents into the keyword/hybrid index. The
	// PostgreSQL adapter is a no-op because BM25 follows the chunk row.
	IndexChunks(ctx context.Context, versionID string, chunks []ChunkDocument) error
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
