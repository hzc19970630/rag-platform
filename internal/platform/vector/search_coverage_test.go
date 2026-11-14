package vector

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestVectorScanLimit(t *testing.T) {
	tests := []struct {
		name       string
		topK       int
		oversample int
		want       int
	}{
		{name: "default oversample", topK: 10, oversample: 0, want: 10},
		{name: "negative oversample", topK: 10, oversample: -3, want: 10},
		{name: "multiplies", topK: 10, oversample: 3, want: 30},
		{name: "caps at the safety valve", topK: 1500, oversample: 4, want: maxVectorLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := vectorScanLimit(test.topK, test.oversample); got != test.want {
				t.Fatalf("vectorScanLimit(%d, %d) = %d, want %d", test.topK, test.oversample, got, test.want)
			}
		})
	}
}

func TestNewSearcherWithMetricsDefaultsRecorder(t *testing.T) {
	searcher := NewSearcherWithMetrics(nil, nil)
	if searcher == nil {
		t.Fatal("NewSearcherWithMetrics returned nil")
	}
	if _, ok := searcher.metrics.(metrics.Nop); !ok {
		t.Fatalf("metrics recorder = %T, want metrics.Nop", searcher.metrics)
	}
}

func TestSearchVectorRejectsInvalidRequests(t *testing.T) {
	searcher := NewSearcherWithMetrics(nil, nil)
	ctx := context.Background()

	if _, err := searcher.SearchVector(ctx, ports.RecallRequest{TopK: 5, KnowledgeBaseIDs: []string{"kb-a"}}, nil); err == nil {
		t.Fatal("empty embedding must be rejected")
	}
	if hits, err := searcher.SearchVector(ctx, ports.RecallRequest{KnowledgeBaseIDs: []string{"kb-a"}}, []float32{1}); err != nil || hits != nil {
		t.Fatalf("zero top-k = %+v, %v; want nil, nil", hits, err)
	}
	if hits, err := searcher.SearchVector(ctx, ports.RecallRequest{TopK: 5}, []float32{1}); err != nil || hits != nil {
		t.Fatalf("empty knowledge bases = %+v, %v; want nil, nil", hits, err)
	}
	if _, err := searcher.SearchVector(ctx, ports.RecallRequest{
		TopK: 5, KnowledgeBaseIDs: []string{"kb-a"}, EFSearch: -1,
	}, []float32{1}); err == nil {
		t.Fatal("negative ef_search must be rejected")
	}
}

func TestSearchVectorAppliesSessionHintsBeforeRecall(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	searcher := NewSearcherWithMetrics(db, nil)
	ctx := context.Background()
	request := ports.RecallRequest{
		TenantID: "tenant-a", KnowledgeBaseIDs: []string{"kb-a"}, EmbeddingModel: "test",
		TopK: 5, EFSearch: 20,
	}
	if _, err := searcher.SearchVector(ctx, request, []float32{0.1, 0.2}); err == nil || !strings.Contains(err.Error(), "set hnsw.ef_search") {
		t.Fatalf("ef_search failure = %v", err)
	}

	request.EFSearch = 0
	request.IterativeScan = true
	if _, err := searcher.SearchVector(ctx, request, []float32{0.1, 0.2}); err == nil || !strings.Contains(err.Error(), "set hnsw.iterative_scan") {
		t.Fatalf("iterative scan failure = %v", err)
	}
}
