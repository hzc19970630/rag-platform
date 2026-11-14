// Postgres-gated online HNSW index tests. They create a scratch schema and
// need a role that can create tables and extensions already installed; run
// with TEST_POSTGRES_URL set, skipped otherwise.
package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/vectorindex"
	"github.com/hzc19970630/rag-platform/internal/platform/vector"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openIndexFixture(t *testing.T) (admin *sql.Conn, gormDB *gorm.DB, schema string) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	schema = fmt.Sprintf("vectorindex_%d", time.Now().UnixNano())

	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	admin, err = pool.Conn(context.Background())
	if err != nil {
		_ = pool.Close()
		t.Fatalf("acquire postgres connection: %v", err)
	}
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create scratch schema: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
		_ = pool.Close()
	})
	gormDB, err = gorm.Open(postgres.Open(withSearchPath(dsn, schema)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := gormDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return admin, gormDB, schema
}

func withSearchPath(dsn, schema string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + url.QueryEscape(schema+",public")
}

func TestOnlineChunksIndexBuildKeepsWritesAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("online index build is a slow integration test")
	}
	admin, gormDB, schema := openIndexFixture(t)
	manager := vector.NewManager(gormDB)
	ctx := context.Background()
	const dimension = 4
	const rows = 200_000

	// parent_id marks searchable child chunks; EnsureChunksIndex reads it when
	// deciding which dimension indexes must survive a rebuild.
	statements := []string{
		fmt.Sprintf("CREATE TABLE %s.chunks (id TEXT PRIMARY KEY, parent_id TEXT, embedding halfvec, embedding_dimension INT NOT NULL)", schema),
		fmt.Sprintf(`INSERT INTO %s.chunks (id, parent_id, embedding, embedding_dimension)
SELECT 'chunk-' || g,
       'parent-' || (g %% 1000),
       ('[' || (g %% 97) || ',' || ((g / 97) %% 89) || ',' || ((g / 7919) %% 83) || ',' || ((g / 7919) %% 79) || ']')::halfvec(%d),
       %d
FROM generate_series(1, %d) g`, schema, dimension, dimension, rows),
	}
	for _, statement := range statements {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fixture setup: %v\n%s", err, statement)
		}
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	insert := func(id string) time.Duration {
		start := time.Now()
		_, err := sqlDB.ExecContext(ctx,
			fmt.Sprintf("INSERT INTO %s.chunks (id, parent_id, embedding, embedding_dimension) VALUES ($1, 'parent-fixture', '[1,2,3,4]'::halfvec(%d), %d)", schema, dimension, dimension),
			id,
		)
		if err != nil {
			t.Errorf("concurrent insert %s: %v", id, err)
		}
		return time.Since(start)
	}

	baseline := make([]time.Duration, 0, 200)
	for i := 0; i < 200; i++ {
		baseline = append(baseline, insert(fmt.Sprintf("baseline-%d", i)))
	}

	buildDone := make(chan error, 1)
	buildStarted := make(chan struct{})
	go func() {
		close(buildStarted)
		_, buildErr := manager.EnsureChunksIndex(ctx, dimension, vectorindex.Options{
			LockTimeout:      30 * time.Second,
			StatementTimeout: 5 * time.Minute,
		})
		buildDone <- buildErr
	}()
	<-buildStarted

	during := make([]time.Duration, 0, 4096)
	buildErr := error(nil)
	finished := false
	for !finished {
		select {
		case buildErr = <-buildDone:
			finished = true
		default:
			during = append(during, insert(fmt.Sprintf("during-%d", len(during))))
		}
	}
	if buildErr != nil {
		t.Fatalf("online index build: %v", buildErr)
	}
	if len(during) < 50 {
		t.Fatalf("build finished too quickly to sample writes (%d samples); increase fixture size", len(during))
	}

	dimensionNow, exists, err := manager.ChunksIndexDimension(ctx)
	if err != nil {
		t.Fatalf("read index dimension: %v", err)
	}
	if !exists || dimensionNow != dimension {
		t.Fatalf("expected a valid index at dimension %d, got exists=%t dimension=%d", dimension, exists, dimensionNow)
	}
	result, err := manager.EnsureChunksIndex(ctx, dimension, vectorindex.Options{})
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if result.Created {
		t.Fatalf("a matching valid index must be a no-op, created %s", result.Index)
	}

	var planLines []string
	// The session setting must run on the same connection as EXPLAIN; a pooled
	// Exec could land elsewhere.
	planConn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire plan connection: %v", err)
	}
	defer planConn.Close()
	if _, err := planConn.ExecContext(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scan: %v", err)
	}
	planRows, err := planConn.QueryContext(ctx,
		fmt.Sprintf("EXPLAIN SELECT id FROM %s.chunks WHERE embedding_dimension = %d ORDER BY embedding::halfvec(%d) <=> '[1,2,3,4]'::halfvec(%d) LIMIT 10", schema, dimension, dimension, dimension),
	)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			planRows.Close()
			t.Fatalf("scan plan: %v", err)
		}
		planLines = append(planLines, line)
	}
	planRows.Close()
	plan := strings.Join(planLines, "\n")
	if !strings.Contains(plan, "chunks_vector_idx_4_v1") || !strings.Contains(plan, "Index Scan") {
		t.Fatalf("query did not use the online HNSW index:\n%s", plan)
	}

	baselineP99 := percentile(baseline, 0.99)
	duringP99 := percentile(during, 0.99)
	limit := 2 * baselineP99
	// A sub-millisecond baseline turns scheduler noise into a false
	// regression; the floor keeps the assertion about lock contention rather
	// than CPU jitter.
	if limit < 5*time.Millisecond {
		limit = 5 * time.Millisecond
	}
	t.Logf("rows=%d baseline_p99=%s during_p99=%s samples=%d limit=%s", rows, baselineP99, duringP99, len(during), limit)
	if duringP99 > limit {
		t.Fatalf("write P99 during rebuild %s exceeds limit %s (baseline %s)", duringP99, limit, baselineP99)
	}
}

func TestChunksIndexDimensionReadsLegacyName(t *testing.T) {
	admin, gormDB, schema := openIndexFixture(t)
	manager := vector.NewManager(gormDB)
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.chunks (id TEXT PRIMARY KEY, parent_id TEXT, embedding halfvec, embedding_dimension INT NOT NULL)", schema)); err != nil {
		t.Fatalf("create chunks: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s.chunks (id, parent_id, embedding, embedding_dimension) VALUES ('legacy-1', 'parent-legacy', '[1,2,3]'::halfvec(3), 3)", schema)); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(
		"CREATE INDEX chunks_vector_idx ON %s.chunks USING hnsw ((embedding::halfvec(3)) halfvec_cosine_ops) WHERE embedding_dimension = 3", schema,
	)); err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	dimension, exists, err := manager.ChunksIndexDimension(ctx)
	if err != nil {
		t.Fatalf("read index dimension: %v", err)
	}
	if !exists || dimension != 3 {
		t.Fatalf("expected the legacy index at dimension 3, got exists=%t dimension=%d", exists, dimension)
	}
	result, err := manager.EnsureChunksIndex(ctx, 3, vectorindex.Options{})
	if err != nil {
		t.Fatalf("ensure on legacy index: %v", err)
	}
	if result.Created || result.Index != vector.ChunksIndexName {
		t.Fatalf("an existing legacy index must be reused, got %+v", result)
	}
}

func percentile(samples []time.Duration, quantile float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}
