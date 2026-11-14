package vector

import (
	"context"

	"github.com/hzc19970630/rag-platform/internal/contracts/vectorindex"
	"gorm.io/gorm"
)

// Manager is the pgvector adapter for the vector index lifecycle port. The
// database handle stays inside the adapter; callers depend on the contract.
type Manager struct {
	db *gorm.DB
}

func NewManager(db *gorm.DB) *Manager { return &Manager{db: db} }

var _ vectorindex.Manager = (*Manager)(nil)

func (m *Manager) EnsureChunksIndex(ctx context.Context, dimension int, options vectorindex.Options) (vectorindex.EnsureResult, error) {
	result, err := EnsureChunksIndex(ctx, m.db, dimension, indexOptions(options))
	return ensureResult(result), err
}

func (m *Manager) EnsureChunkEmbeddingIndex(ctx context.Context, dimension int, options vectorindex.Options) (vectorindex.EnsureResult, error) {
	result, err := EnsureChunkEmbeddingIndex(ctx, m.db, dimension, indexOptions(options))
	return ensureResult(result), err
}

func (m *Manager) CheckChunksIndexes(ctx context.Context) (vectorindex.CheckReport, error) {
	report, err := CheckChunksIndexes(ctx, m.db)
	return checkReport(report), err
}

func (m *Manager) CheckChunkEmbeddingIndexes(ctx context.Context) (vectorindex.EmbeddingIndexReport, error) {
	report, err := CheckChunkEmbeddingIndexes(ctx, m.db)
	return embeddingReport(report), err
}

func (m *Manager) ChunksIndexDimension(ctx context.Context) (int, bool, error) {
	return ChunksIndexDimension(ctx, m.db)
}

func indexOptions(options vectorindex.Options) IndexOptions {
	return IndexOptions{
		LockTimeout: options.LockTimeout, StatementTimeout: options.StatementTimeout,
		DryRun: options.DryRun, KeepDimensions: options.KeepDimensions,
	}
}

func ensureResult(result EnsureResult) vectorindex.EnsureResult {
	return vectorindex.EnsureResult{
		Dimension: result.Dimension, Index: result.Index, Created: result.Created,
		Planned: result.Planned, Dropped: result.Dropped, SizeBytes: result.SizeBytes, Duration: result.Duration,
	}
}

func indexStatuses(items []IndexStatus) []vectorindex.IndexStatus {
	result := make([]vectorindex.IndexStatus, 0, len(items))
	for _, item := range items {
		result = append(result, vectorindex.IndexStatus{
			Name: item.Name, Dimension: item.Dimension, Version: item.Version, Valid: item.Valid,
			Definition: item.Definition, SizeBytes: item.SizeBytes,
		})
	}
	return result
}

func dimensionCounts(items []DimensionCount) []vectorindex.DimensionCount {
	result := make([]vectorindex.DimensionCount, 0, len(items))
	for _, item := range items {
		result = append(result, vectorindex.DimensionCount{Dimension: item.Dimension, Count: item.Count})
	}
	return result
}

func checkReport(report CheckReport) vectorindex.CheckReport {
	return vectorindex.CheckReport{
		ProviderVersion: report.PgvectorVersion, Indexes: indexStatuses(report.Indexes),
		DimensionHistogram: dimensionCounts(report.DimensionHistogram),
		DatabaseSizeBytes:  report.DatabaseSizeBytes, HeapBytes: report.ChunksHeapBytes,
		DiskFreeBytes: report.DiskFreeBytes, DiskFreeScope: report.DiskFreeScope,
	}
}

func embeddingReport(report EmbeddingIndexReport) vectorindex.EmbeddingIndexReport {
	return vectorindex.EmbeddingIndexReport{
		Indexes: indexStatuses(report.Indexes), DimensionHistogram: dimensionCounts(report.DimensionHistogram),
	}
}
