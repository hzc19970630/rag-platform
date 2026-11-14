package vector

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeInspectSession struct {
	version      string
	versionErr   error
	indexes      []indexRecord
	listErr      error
	sizes        map[string]int64
	sizeErr      error
	databaseSize int64
	databaseErr  error
	heapSize     int64
	heapErr      error
	histogram    []DimensionCount
	histogramErr error
}

func (f *fakeInspectSession) PgvectorVersion(context.Context) (string, error) {
	return f.version, f.versionErr
}

func (f *fakeInspectSession) ChunkIndexes(context.Context) ([]indexRecord, error) {
	return f.indexes, f.listErr
}

func (f *fakeInspectSession) IndexSizeBytes(_ context.Context, name string) (int64, error) {
	return f.sizes[name], f.sizeErr
}

func (f *fakeInspectSession) DatabaseSize(context.Context) (int64, error) {
	return f.databaseSize, f.databaseErr
}

func (f *fakeInspectSession) ChunksHeapSize(context.Context) (int64, error) {
	return f.heapSize, f.heapErr
}

func (f *fakeInspectSession) DimensionHistogram(context.Context) ([]DimensionCount, error) {
	return f.histogram, f.histogramErr
}

func TestCheckChunksIndexesAssemblesReport(t *testing.T) {
	session := &fakeInspectSession{
		version: "0.8.7",
		indexes: []indexRecord{
			{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 8)},
			{Name: "chunks_vector_idx_8_v2", Valid: false, Definition: indexDefinition("chunks_vector_idx_8_v2", 8)},
			{Name: "unrelated_idx", Valid: true, Definition: "CREATE INDEX unrelated_idx ON chunks (status)"},
		},
		sizes:        map[string]int64{ChunksIndexName: 10, "chunks_vector_idx_8_v2": 20},
		databaseSize: 1000,
		heapSize:     500,
		histogram:    []DimensionCount{{Dimension: 8, Count: 7}},
	}
	report, err := checkChunksIndexes(context.Background(), session)
	if err != nil {
		t.Fatalf("checkChunksIndexes: %v", err)
	}
	if report.PgvectorVersion != "0.8.7" || report.DatabaseSizeBytes != 1000 || report.ChunksHeapBytes != 500 {
		t.Fatalf("report totals = %+v", report)
	}
	if report.DiskFreeBytes == 0 || report.DiskFreeScope != "embedctl-container-root" {
		t.Fatalf("disk free = %+v", report)
	}
	if len(report.Indexes) != 3 {
		t.Fatalf("indexes = %+v", report.Indexes)
	}
	if report.Indexes[0].Version != -1 || report.Indexes[0].Dimension != 8 || report.Indexes[0].SizeBytes != 10 {
		t.Fatalf("legacy index = %+v", report.Indexes[0])
	}
	if report.Indexes[1].Version != 2 || report.Indexes[1].Valid {
		t.Fatalf("versioned index = %+v", report.Indexes[1])
	}
	if report.Indexes[2].Dimension != 0 {
		t.Fatalf("unparsable definition must leave dimension zero: %+v", report.Indexes[2])
	}
	if len(report.DimensionHistogram) != 1 || report.DimensionHistogram[0].Count != 7 {
		t.Fatalf("histogram = %+v", report.DimensionHistogram)
	}
}

func TestCheckChunksIndexesFailurePaths(t *testing.T) {
	tests := []struct {
		name    string
		session *fakeInspectSession
		want    string
	}{
		{name: "version", session: &fakeInspectSession{versionErr: fmt.Errorf("version down")}, want: "version down"},
		{name: "catalog", session: &fakeInspectSession{listErr: fmt.Errorf("catalog down")}, want: "catalog down"},
		{name: "database size", session: &fakeInspectSession{databaseErr: fmt.Errorf("size down")}, want: "read database size"},
		{name: "heap size", session: &fakeInspectSession{heapErr: fmt.Errorf("heap down")}, want: "read chunks heap size"},
		{name: "histogram", session: &fakeInspectSession{histogramErr: fmt.Errorf("histogram down")}, want: "read embedding dimension histogram"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := checkChunksIndexes(context.Background(), test.session); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("checkChunksIndexes error = %v, want %q", err, test.want)
			}
		})
	}

	// A size lookup failure only degrades the report; it must not fail the check.
	report, err := checkChunksIndexes(context.Background(), &fakeInspectSession{
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 8)}},
		sizeErr: fmt.Errorf("size unavailable"),
	})
	if err != nil {
		t.Fatalf("size failure must not fail the check: %v", err)
	}
	if len(report.Indexes) != 1 || report.Indexes[0].SizeBytes != 0 {
		t.Fatalf("report with size failure = %+v", report.Indexes)
	}
}
