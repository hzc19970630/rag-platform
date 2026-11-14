// Package vectorindex is the storage-neutral contract for vector index
// lifecycle management. The engine builds, inspects and reports indexes through
// this port; a PostgreSQL/pgvector adapter implements it today, and an external
// vector store can implement the same contract without touching domain code.
package vectorindex

import (
	"context"
	"time"
)

// Options controls an online index build. Zero timeouts mean "no limit".
type Options struct {
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	DryRun           bool
	// KeepDimensions names dimensions that still have searchable vectors. The
	// manager must not drop their indexes while switching to another dimension.
	KeepDimensions map[int]bool
}

// EnsureResult describes what an index build did. In a dry run Created is
// false and Dropped lists the indexes a real run would remove.
type EnsureResult struct {
	Dimension int
	Index     string
	Created   bool
	Planned   bool
	Dropped   []string
	SizeBytes int64
	Duration  time.Duration
}

// IndexStatus is one vector index reported by an operator check.
type IndexStatus struct {
	Name       string
	Dimension  int
	Version    int
	Valid      bool
	Definition string
	SizeBytes  int64
}

// DimensionCount is one bucket of the stored-vector dimension histogram.
type DimensionCount struct {
	Dimension int
	Count     int64
}

// CheckReport is the read-only state an operator needs before switching the
// embedding dimension.
type CheckReport struct {
	ProviderVersion    string
	Indexes            []IndexStatus
	DimensionHistogram []DimensionCount
	DatabaseSizeBytes  int64
	HeapBytes          int64
	DiskFreeBytes      uint64
	DiskFreeScope      string
}

// EmbeddingIndexReport describes the variant indexes and stored dimensions.
type EmbeddingIndexReport struct {
	Indexes            []IndexStatus
	DimensionHistogram []DimensionCount
}

// Manager owns vector index lifecycle operations for one deployment. It does
// not expose a database handle or SQL types: adapters keep their own connection
// and translate results into this contract.
type Manager interface {
	// EnsureChunksIndex builds or replaces the legacy chunk vector index.
	EnsureChunksIndex(ctx context.Context, dimension int, options Options) (EnsureResult, error)
	// EnsureChunkEmbeddingIndex builds the variant-table index for one dimension.
	EnsureChunkEmbeddingIndex(ctx context.Context, dimension int, options Options) (EnsureResult, error)
	// CheckChunksIndexes reports the legacy chunk index state.
	CheckChunksIndexes(ctx context.Context) (CheckReport, error)
	// CheckChunkEmbeddingIndexes reports the variant index state.
	CheckChunkEmbeddingIndexes(ctx context.Context) (EmbeddingIndexReport, error)
	// ChunksIndexDimension reports the serving legacy index dimension.
	ChunksIndexDimension(ctx context.Context) (int, bool, error)
}
