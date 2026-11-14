package vector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeVariantSession struct {
	version      string
	versionErr   error
	indexes      []indexRecord
	listErr      error
	histogram    []DimensionCount
	histogramErr error
	execErr      func(statement string) error
	execCalls    []string
}

func (f *fakeVariantSession) PgvectorVersion(context.Context) (string, error) {
	return f.version, f.versionErr
}

func (f *fakeVariantSession) ListIndexes(context.Context) ([]indexRecord, error) {
	return append([]indexRecord(nil), f.indexes...), f.listErr
}

func (f *fakeVariantSession) DimensionHistogram(context.Context) ([]DimensionCount, error) {
	return append([]DimensionCount(nil), f.histogram...), f.histogramErr
}

func (f *fakeVariantSession) Exec(_ context.Context, statement string) error {
	f.execCalls = append(f.execCalls, statement)
	if f.execErr != nil {
		return f.execErr(statement)
	}
	return nil
}

func variantIndexDefinition(name string, dimension int) string {
	return fmt.Sprintf(
		"CREATE INDEX %s ON public.chunk_embeddings USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) WHERE (dimension = %d)",
		name, dimension, dimension,
	)
}

func TestParseEmbeddingIndexName(t *testing.T) {
	tests := []struct {
		name          string
		wantDimension int
		wantVersion   int
		wantOK        bool
	}{
		{name: "chunk_embeddings_1024_v1", wantDimension: 1024, wantVersion: 1, wantOK: true},
		{name: "chunk_embeddings_8_v12", wantDimension: 8, wantVersion: 12, wantOK: true},
		{name: "chunk_embeddings_8", wantOK: false},
		{name: "chunk_embeddings_x_v1", wantOK: false},
		{name: "chunk_embeddings_8_vx", wantOK: false},
		{name: "chunk_embeddings_scope_dimension", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dimension, version, ok := parseEmbeddingIndexName(test.name)
			if ok != test.wantOK || dimension != test.wantDimension || version != test.wantVersion {
				t.Fatalf("parseEmbeddingIndexName(%q) = %d, %d, %v", test.name, dimension, version, ok)
			}
		})
	}
}

func TestNextEmbeddingIndexName(t *testing.T) {
	tests := []struct {
		name    string
		records []indexRecord
		want    string
	}{
		{name: "empty catalog", want: "chunk_embeddings_8_v1"},
		{
			name: "advances only the matching dimension",
			records: []indexRecord{
				{Name: "chunk_embeddings_8_v1", Valid: true},
				{Name: "chunk_embeddings_8_v3", Valid: true},
				{Name: "chunk_embeddings_1024_v9", Valid: true},
			},
			want: "chunk_embeddings_8_v4",
		},
		{
			name: "ignores invalid and non-versioned records",
			records: []indexRecord{
				{Name: "chunk_embeddings_8_v7", Valid: false},
				{Name: "chunk_embeddings_scope", Valid: true},
			},
			want: "chunk_embeddings_8_v1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextEmbeddingIndexName(test.records, 8); got != test.want {
				t.Fatalf("nextEmbeddingIndexName = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEnsureChunkEmbeddingIndexValidation(t *testing.T) {
	if _, err := ensureChunkEmbeddingIndex(context.Background(), &fakeVariantSession{}, 0, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("zero dimension = %v, want positive-integer error", err)
	}
	session := &fakeVariantSession{versionErr: fmt.Errorf("boom")}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), session, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "read pgvector version") {
		t.Fatalf("version error = %v", err)
	}
	session = &fakeVariantSession{version: "0.6.9"}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), session, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("old pgvector = %v, want too-old error", err)
	}

	session = &fakeVariantSession{version: "0.8.7", execErr: func(statement string) error {
		if strings.HasPrefix(statement, "SET lock_timeout") {
			return fmt.Errorf("lock timeout denied")
		}
		return nil
	}}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), session, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "configure index build session") {
		t.Fatalf("lock timeout configure error = %v", err)
	}

	session = &fakeVariantSession{version: "0.8.7", execErr: func(statement string) error {
		if strings.HasPrefix(statement, "SET statement_timeout") {
			return fmt.Errorf("statement timeout denied")
		}
		return nil
	}}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), session, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "configure index build session") {
		t.Fatalf("statement timeout configure error = %v", err)
	}
	if len(session.execCalls) < 3 || session.execCalls[len(session.execCalls)-1] != "RESET lock_timeout" {
		t.Fatalf("lock timeout must be reset after a partial configure: %v", session.execCalls)
	}
}

func TestEnsureChunkEmbeddingIndexNoOpAndDryRun(t *testing.T) {
	session := &fakeVariantSession{
		version: "0.8.7",
		indexes: []indexRecord{
			{Name: "chunk_embeddings_scope_dimension", Valid: true, Definition: "CREATE INDEX chunk_embeddings_scope_dimension ON chunk_embeddings (dimension)"},
			{Name: "chunk_embeddings_8_v2", Valid: true, Definition: variantIndexDefinition("chunk_embeddings_8_v2", 8)},
		},
	}
	result, err := ensureChunkEmbeddingIndex(context.Background(), session, 8, IndexOptions{})
	if err != nil {
		t.Fatalf("no-op ensure: %v", err)
	}
	if result.Index != "chunk_embeddings_8_v2" || result.Created || result.Planned {
		t.Fatalf("no-op result = %+v", result)
	}
	for _, call := range session.execCalls {
		if strings.HasPrefix(call, "CREATE INDEX") {
			t.Fatalf("no-op must not create an index: %v", session.execCalls)
		}
	}

	dryRun := &fakeVariantSession{version: "0.8.7"}
	result, err = ensureChunkEmbeddingIndex(context.Background(), dryRun, 8, IndexOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !result.Planned || result.Index != "chunk_embeddings_8_v1" || result.Created {
		t.Fatalf("dry run result = %+v", result)
	}
}

func TestEnsureChunkEmbeddingIndexCreateAndVerify(t *testing.T) {
	session := &fakeVariantSession{version: "0.8.7"}
	session.execErr = func(statement string) error {
		if strings.HasPrefix(statement, "CREATE INDEX CONCURRENTLY") {
			fields := strings.Fields(statement)
			name := strings.Trim(fields[3], `"`)
			session.indexes = append(session.indexes, indexRecord{Name: name, Valid: true, Definition: variantIndexDefinition(name, 8)})
		}
		return nil
	}
	result, err := ensureChunkEmbeddingIndex(context.Background(), session, 8, IndexOptions{LockTimeout: time.Second})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !result.Created || result.Index != "chunk_embeddings_8_v1" {
		t.Fatalf("create result = %+v", result)
	}
	resets := 0
	for _, call := range session.execCalls {
		if strings.HasPrefix(call, "RESET ") {
			resets++
		}
	}
	if resets != 2 {
		t.Fatalf("session settings must be reset twice, got %v", session.execCalls)
	}
}

func TestEnsureChunkEmbeddingIndexFailurePaths(t *testing.T) {
	createFailure := &fakeVariantSession{version: "0.8.7", execErr: func(statement string) error {
		if strings.HasPrefix(statement, "CREATE INDEX CONCURRENTLY") {
			return fmt.Errorf("build failed")
		}
		return nil
	}}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), createFailure, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "create chunk embedding index at dimension 8") {
		t.Fatalf("create failure = %v", err)
	}
	cleanup := false
	for _, call := range createFailure.execCalls {
		if strings.HasPrefix(call, "DROP INDEX CONCURRENTLY IF EXISTS") {
			cleanup = true
		}
	}
	if !cleanup {
		t.Fatalf("failed create must clean up the invalid index: %v", createFailure.execCalls)
	}

	missing := &fakeVariantSession{version: "0.8.7"}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), missing, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "missing from the catalog") {
		t.Fatalf("missing index = %v", err)
	}

	invalid := &fakeVariantSession{version: "0.8.7"}
	invalid.execErr = func(statement string) error {
		if strings.HasPrefix(statement, "CREATE INDEX CONCURRENTLY") {
			fields := strings.Fields(statement)
			name := strings.Trim(fields[3], `"`)
			invalid.indexes = append(invalid.indexes, indexRecord{Name: name, Valid: false, Definition: variantIndexDefinition(name, 8)})
		}
		return nil
	}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), invalid, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "did not validate after build") {
		t.Fatalf("invalid index = %v", err)
	}

	unparsable := &fakeVariantSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: "chunk_embeddings_8_v1", Valid: true, Definition: "CREATE INDEX broken"}},
	}
	if _, err := ensureChunkEmbeddingIndex(context.Background(), unparsable, 8, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "cannot parse halfvec dimension") {
		t.Fatalf("unparsable definition = %v", err)
	}
}

func TestCheckChunkEmbeddingIndexesCore(t *testing.T) {
	session := &fakeVariantSession{
		indexes: []indexRecord{
			{Name: "chunk_embeddings_scope_dimension", Valid: true, Definition: "CREATE INDEX chunk_embeddings_scope_dimension ON chunk_embeddings (dimension)"},
			{Name: "chunk_embeddings_8_v1", Valid: true, Definition: variantIndexDefinition("chunk_embeddings_8_v1", 8)},
		},
		histogram: []DimensionCount{{Dimension: 8, Count: 3}},
	}
	report, err := checkChunkEmbeddingIndexes(context.Background(), session)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(report.Indexes) != 1 || report.Indexes[0].Name != "chunk_embeddings_8_v1" || report.Indexes[0].Dimension != 8 || report.Indexes[0].Version != 1 {
		t.Fatalf("report indexes = %+v", report.Indexes)
	}
	if len(report.DimensionHistogram) != 1 || report.DimensionHistogram[0].Count != 3 {
		t.Fatalf("histogram = %+v", report.DimensionHistogram)
	}

	if _, err := checkChunkEmbeddingIndexes(context.Background(), &fakeVariantSession{listErr: fmt.Errorf("catalog down")}); err == nil || !strings.Contains(err.Error(), "catalog down") {
		t.Fatalf("list error = %v", err)
	}
	if _, err := checkChunkEmbeddingIndexes(context.Background(), &fakeVariantSession{histogramErr: fmt.Errorf("scan failed")}); err == nil || !strings.Contains(err.Error(), "read chunk embedding dimensions") {
		t.Fatalf("histogram error = %v", err)
	}
}

func TestVariantFunctionsUseDatabaseHandle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := CheckChunkEmbeddingIndexes(context.Background(), db); err == nil {
		t.Fatal("CheckChunkEmbeddingIndexes must fail without the pgvector catalog")
	}
	if _, err := EnsureChunkEmbeddingIndex(context.Background(), db, 0, IndexOptions{}); err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("zero dimension = %v", err)
	}
	if _, err := EnsureChunkEmbeddingIndex(context.Background(), db, 8, IndexOptions{}); err == nil {
		t.Fatal("EnsureChunkEmbeddingIndex must fail without the pgvector extension")
	}
}
