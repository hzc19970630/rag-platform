package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/vectorindex"
	"github.com/hzc19970630/rag-platform/internal/embeddingmigration"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
)

type fakeVectorManager struct {
	embeddingDimension int
	embeddingOptions   vectorindex.Options
	err                error
}

func (f *fakeVectorManager) EnsureChunksIndex(context.Context, int, vectorindex.Options) (vectorindex.EnsureResult, error) {
	return vectorindex.EnsureResult{}, f.err
}

func (f *fakeVectorManager) EnsureChunkEmbeddingIndex(_ context.Context, dimension int, options vectorindex.Options) (vectorindex.EnsureResult, error) {
	f.embeddingDimension = dimension
	f.embeddingOptions = options
	return vectorindex.EnsureResult{Dimension: dimension}, f.err
}

func (f *fakeVectorManager) CheckChunksIndexes(context.Context) (vectorindex.CheckReport, error) {
	return vectorindex.CheckReport{}, f.err
}

func (f *fakeVectorManager) CheckChunkEmbeddingIndexes(context.Context) (vectorindex.EmbeddingIndexReport, error) {
	return vectorindex.EmbeddingIndexReport{}, f.err
}

func (f *fakeVectorManager) ChunksIndexDimension(context.Context) (int, bool, error) {
	return 0, false, f.err
}

func TestEmbeddingIndexEnsurerForwardsDimensionAndTimeouts(t *testing.T) {
	manager := &fakeVectorManager{}
	ensurer := embeddingIndexEnsurer{
		manager:          manager,
		lockTimeout:      5 * time.Second,
		statementTimeout: 10 * time.Second,
	}
	if err := ensurer.EnsureIndex(context.Background(), 8); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}
	if manager.embeddingDimension != 8 {
		t.Fatalf("dimension = %d, want 8", manager.embeddingDimension)
	}
	if manager.embeddingOptions.LockTimeout != 5*time.Second || manager.embeddingOptions.StatementTimeout != 10*time.Second {
		t.Fatalf("options = %+v", manager.embeddingOptions)
	}

	manager.err = errors.New("index build failed")
	if err := ensurer.EnsureIndex(context.Background(), 8); err == nil || !errors.Is(err, manager.err) {
		t.Fatalf("EnsureIndex error = %v", err)
	}
}

type fakeVariantSink struct {
	model   string
	records []evidenceindex.VariantRecord
	err     error
}

func (f *fakeVariantSink) WriteVariants(_ context.Context, model string, records []evidenceindex.VariantRecord) error {
	f.model = model
	f.records = append([]evidenceindex.VariantRecord(nil), records...)
	return f.err
}

func TestVariantWriterConvertsRecords(t *testing.T) {
	sink := &fakeVariantSink{}
	writer := variantWriter{writer: sink}
	records := []embeddingmigration.VariantRecord{
		{ChunkID: "chunk-1", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", Dimension: 8, Vector: []float32{0.1, 0.2}},
	}
	if err := writer.WriteVariants(context.Background(), "target-embed", records); err != nil {
		t.Fatalf("WriteVariants: %v", err)
	}
	if sink.model != "target-embed" || len(sink.records) != 1 {
		t.Fatalf("sink = %+v", sink)
	}
	converted := sink.records[0]
	if converted.ChunkID != "chunk-1" || converted.TenantID != "tenant-1" || converted.KnowledgeBaseID != "kb-1" || converted.Dimension != 8 || len(converted.Vector) != 2 {
		t.Fatalf("converted record = %+v", converted)
	}

	if err := writer.WriteVariants(context.Background(), "target-embed", nil); err != nil {
		t.Fatalf("empty WriteVariants: %v", err)
	}
	if len(sink.records) != 0 {
		t.Fatalf("empty records must pass through: %+v", sink.records)
	}

	sink.err = errors.New("write failed")
	if err := writer.WriteVariants(context.Background(), "target-embed", records); err == nil || !errors.Is(err, sink.err) {
		t.Fatalf("WriteVariants error = %v", err)
	}
}
