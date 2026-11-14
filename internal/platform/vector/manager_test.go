package vector

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/contracts/vectorindex"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestIndexOptionsMapping(t *testing.T) {
	options := indexOptions(vectorindex.Options{
		LockTimeout:      time.Second,
		StatementTimeout: 2 * time.Second,
		DryRun:           true,
		KeepDimensions:   map[int]bool{8: true},
	})
	want := IndexOptions{
		LockTimeout:      time.Second,
		StatementTimeout: 2 * time.Second,
		DryRun:           true,
		KeepDimensions:   map[int]bool{8: true},
	}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("indexOptions = %+v, want %+v", options, want)
	}
}

func TestEnsureResultMapping(t *testing.T) {
	input := EnsureResult{
		Dimension: 8, Index: "idx", Created: true, Planned: false,
		Dropped: []string{"old"}, SizeBytes: 42, Duration: time.Second,
	}
	got := ensureResult(input)
	want := vectorindex.EnsureResult{
		Dimension: 8, Index: "idx", Created: true, Planned: false,
		Dropped: []string{"old"}, SizeBytes: 42, Duration: time.Second,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ensureResult = %+v, want %+v", got, want)
	}
}

func TestReportMapping(t *testing.T) {
	statuses := indexStatuses([]IndexStatus{{Name: "idx", Dimension: 8, Version: 2, Valid: true, Definition: "def", SizeBytes: 10}})
	if len(statuses) != 1 || statuses[0].Dimension != 8 || statuses[0].Version != 2 || statuses[0].SizeBytes != 10 {
		t.Fatalf("indexStatuses = %+v", statuses)
	}
	counts := dimensionCounts([]DimensionCount{{Dimension: 8, Count: 4}})
	if len(counts) != 1 || counts[0].Dimension != 8 || counts[0].Count != 4 {
		t.Fatalf("dimensionCounts = %+v", counts)
	}

	report := checkReport(CheckReport{
		PgvectorVersion:    "0.8.7",
		Indexes:            []IndexStatus{{Name: "idx", Dimension: 8}},
		DimensionHistogram: []DimensionCount{{Dimension: 8, Count: 2}},
		DatabaseSizeBytes:  100,
		ChunksHeapBytes:    50,
		DiskFreeBytes:      1000,
		DiskFreeScope:      "rootfs",
	})
	if report.ProviderVersion != "0.8.7" || report.DatabaseSizeBytes != 100 || report.HeapBytes != 50 || report.DiskFreeBytes != 1000 || report.DiskFreeScope != "rootfs" {
		t.Fatalf("checkReport = %+v", report)
	}
	if len(report.Indexes) != 1 || len(report.DimensionHistogram) != 1 {
		t.Fatalf("checkReport slices = %+v", report)
	}

	embedding := embeddingReport(EmbeddingIndexReport{
		Indexes:            []IndexStatus{{Name: "variant", Dimension: 8}},
		DimensionHistogram: []DimensionCount{{Dimension: 8, Count: 5}},
	})
	if len(embedding.Indexes) != 1 || len(embedding.DimensionHistogram) != 1 {
		t.Fatalf("embeddingReport = %+v", embedding)
	}

	if len(indexStatuses(nil)) != 0 || len(dimensionCounts(nil)) != 0 {
		t.Fatal("nil inputs must map to empty slices")
	}
}

func TestManagerDelegatesToDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	manager := NewManager(db)
	ctx := context.Background()
	if _, err := manager.EnsureChunksIndex(ctx, 8, vectorindex.Options{}); err == nil {
		t.Fatal("EnsureChunksIndex must fail without the pgvector catalog")
	}
	if _, err := manager.EnsureChunkEmbeddingIndex(ctx, 8, vectorindex.Options{}); err == nil {
		t.Fatal("EnsureChunkEmbeddingIndex must fail without the pgvector extension")
	}
	if _, err := manager.CheckChunksIndexes(ctx); err == nil {
		t.Fatal("CheckChunksIndexes must fail without the pgvector catalog")
	}
	if _, err := manager.CheckChunkEmbeddingIndexes(ctx); err == nil {
		t.Fatal("CheckChunkEmbeddingIndexes must fail without the pgvector catalog")
	}
	if _, _, err := manager.ChunksIndexDimension(ctx); err == nil {
		t.Fatal("ChunksIndexDimension must fail without the pgvector catalog")
	}
}
