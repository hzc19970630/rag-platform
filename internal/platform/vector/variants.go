package vector

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// EmbeddingIndexPrefix groups the HNSW indexes over chunk_embeddings. Each
// dimension gets its own partial index, so a migration can build the target
// space without touching the source index.
const EmbeddingIndexPrefix = "chunk_embeddings"

var embeddingIndexNamePattern = regexp.MustCompile(`^chunk_embeddings_(\d+)_v(\d+)$`)

const chunkEmbeddingIndexesQuery = `
SELECT c.relname, i.indisvalid, pg_get_indexdef(i.indexrelid)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'chunk_embeddings'
  AND left(c.relname, length($1::text)) = $1::text
ORDER BY c.relname`

// EmbeddingIndexReport describes the variant-table indexes and the stored
// dimensions, which is what the migration job needs before it starts.
type EmbeddingIndexReport struct {
	Indexes            []IndexStatus
	DimensionHistogram []DimensionCount
}

// variantSession is the subset of a dedicated PostgreSQL connection used by
// the variant index lifecycle. It mirrors indexSession so the catalog and
// failure paths can be exercised without a live database.
type variantSession interface {
	PgvectorVersion(ctx context.Context) (string, error)
	ListIndexes(ctx context.Context) ([]indexRecord, error)
	DimensionHistogram(ctx context.Context) ([]DimensionCount, error)
	Exec(ctx context.Context, statement string) error
}

type postgresVariantSession struct {
	connection *sql.Conn
}

func (s postgresVariantSession) PgvectorVersion(ctx context.Context) (string, error) {
	var version string
	if err := s.connection.QueryRowContext(ctx, pgvectorVersionQuery).Scan(&version); err != nil {
		return "", err
	}
	return version, nil
}

func (s postgresVariantSession) ListIndexes(ctx context.Context) ([]indexRecord, error) {
	return listEmbeddingIndexes(ctx, s.connection)
}

func (s postgresVariantSession) DimensionHistogram(ctx context.Context) ([]DimensionCount, error) {
	rows, err := s.connection.QueryContext(ctx, "SELECT dimension, count(*) FROM chunk_embeddings GROUP BY dimension ORDER BY dimension")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var histogram []DimensionCount
	for rows.Next() {
		var bucket DimensionCount
		if err := rows.Scan(&bucket.Dimension, &bucket.Count); err != nil {
			return nil, fmt.Errorf("scan chunk embedding dimension: %w", err)
		}
		histogram = append(histogram, bucket)
	}
	return histogram, rows.Err()
}

func (s postgresVariantSession) Exec(ctx context.Context, statement string) error {
	_, err := s.connection.ExecContext(ctx, statement)
	return err
}

func openVariantSession(ctx context.Context, db *gorm.DB, purpose string) (variantSession, func(), error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("database handle: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire %s connection: %w", purpose, err)
	}
	return postgresVariantSession{connection: connection}, func() { _ = connection.Close() }, nil
}

func CheckChunkEmbeddingIndexes(ctx context.Context, db *gorm.DB) (EmbeddingIndexReport, error) {
	session, closeSession, err := openVariantSession(ctx, db, "check")
	if err != nil {
		return EmbeddingIndexReport{}, err
	}
	defer closeSession()
	return checkChunkEmbeddingIndexes(ctx, session)
}

func checkChunkEmbeddingIndexes(ctx context.Context, session variantSession) (EmbeddingIndexReport, error) {
	var report EmbeddingIndexReport
	records, err := session.ListIndexes(ctx)
	if err != nil {
		return report, err
	}
	for _, record := range records {
		// The prefix also matches the scope/dimension btree indexes. Only the
		// versioned HNSW names are vector indexes.
		if _, versionNumber, ok := parseEmbeddingIndexName(record.Name); !ok {
			continue
		} else {
			status := IndexStatus{Name: record.Name, Valid: record.Valid, Definition: record.Definition, Version: versionNumber}
			if dimension, parseErr := dimensionFromIndexDef(record.Definition); parseErr == nil {
				status.Dimension = dimension
			}
			report.Indexes = append(report.Indexes, status)
		}
	}
	histogram, err := session.DimensionHistogram(ctx)
	if err != nil {
		return report, fmt.Errorf("read chunk embedding dimensions: %w", err)
	}
	report.DimensionHistogram = histogram
	return report, nil
}

// EnsureChunkEmbeddingIndex builds the variant HNSW index for one dimension.
// It never drops another dimension's index: the source space must stay
// searchable until the migration is cut over.
func EnsureChunkEmbeddingIndex(ctx context.Context, db *gorm.DB, dimension int, options IndexOptions) (EnsureResult, error) {
	if dimension <= 0 {
		return EnsureResult{}, fmt.Errorf("vector dimension must be a positive integer, got %d", dimension)
	}
	session, closeSession, err := openVariantSession(ctx, db, "index build")
	if err != nil {
		return EnsureResult{}, err
	}
	defer closeSession()
	return ensureChunkEmbeddingIndex(ctx, session, dimension, options)
}

func ensureChunkEmbeddingIndex(ctx context.Context, session variantSession, dimension int, options IndexOptions) (result EnsureResult, err error) {
	result = EnsureResult{Dimension: dimension}
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	if dimension <= 0 {
		return result, fmt.Errorf("vector dimension must be a positive integer, got %d", dimension)
	}
	version, err := session.PgvectorVersion(ctx)
	if err != nil {
		return result, fmt.Errorf("read pgvector version: %w", err)
	}
	if err := checkPgvectorVersion(version); err != nil {
		return result, err
	}
	if err := session.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", options.LockTimeout.Milliseconds())); err != nil {
		return result, fmt.Errorf("configure index build session: %w", err)
	}
	defer func() {
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = session.Exec(resetCtx, "RESET lock_timeout")
	}()
	if err := session.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", options.StatementTimeout.Milliseconds())); err != nil {
		return result, fmt.Errorf("configure index build session: %w", err)
	}
	defer func() {
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = session.Exec(resetCtx, "RESET statement_timeout")
	}()

	records, err := session.ListIndexes(ctx)
	if err != nil {
		return result, err
	}
	for _, record := range records {
		if !record.Valid {
			continue
		}
		// Skip the scope/dimension btree indexes; only the versioned HNSW
		// indexes carry a halfvec dimension.
		if _, _, ok := parseEmbeddingIndexName(record.Name); !ok {
			continue
		}
		current, parseErr := dimensionFromIndexDef(record.Definition)
		if parseErr != nil {
			return result, parseErr
		}
		if current == dimension {
			result.Index = record.Name
			return result, nil
		}
	}
	if options.DryRun {
		result.Index = nextEmbeddingIndexName(records, dimension)
		result.Planned = true
		return result, nil
	}
	target := nextEmbeddingIndexName(records, dimension)
	statement := fmt.Sprintf(
		"CREATE INDEX CONCURRENTLY %s ON chunk_embeddings USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE dimension = %d",
		quoteIndexName(target), dimension, dimension,
	)
	if err := session.Exec(ctx, statement); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = session.Exec(cleanupCtx, "DROP INDEX CONCURRENTLY IF EXISTS "+quoteIndexName(target))
		return result, fmt.Errorf("create chunk embedding index at dimension %d: %w", dimension, err)
	}
	records, err = session.ListIndexes(ctx)
	if err != nil {
		return result, err
	}
	for _, record := range records {
		if record.Name != target {
			continue
		}
		current, parseErr := dimensionFromIndexDef(record.Definition)
		if parseErr != nil {
			return result, parseErr
		}
		if !record.Valid || current != dimension {
			return result, fmt.Errorf("index %s did not validate after build (valid=%t dimension=%d)", target, record.Valid, current)
		}
		result.Index = target
		result.Created = true
		return result, nil
	}
	return result, fmt.Errorf("index %s is missing from the catalog after build", target)
}

func listEmbeddingIndexes(ctx context.Context, connection *sql.Conn) ([]indexRecord, error) {
	rows, err := connection.QueryContext(ctx, chunkEmbeddingIndexesQuery, EmbeddingIndexPrefix)
	if err != nil {
		return nil, fmt.Errorf("read chunk embedding indexes: %w", err)
	}
	defer rows.Close()
	var records []indexRecord
	for rows.Next() {
		var record indexRecord
		if err := rows.Scan(&record.Name, &record.Valid, &record.Definition); err != nil {
			return nil, fmt.Errorf("scan chunk embedding index: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func nextEmbeddingIndexName(records []indexRecord, dimension int) string {
	version := 0
	for _, record := range records {
		if !record.Valid {
			continue
		}
		indexDimension, indexVersion, ok := parseEmbeddingIndexName(record.Name)
		if !ok || indexDimension != dimension {
			continue
		}
		if indexVersion > version {
			version = indexVersion
		}
	}
	return fmt.Sprintf("%s_%d_v%d", EmbeddingIndexPrefix, dimension, version+1)
}

func parseEmbeddingIndexName(name string) (dimension int, version int, ok bool) {
	match := embeddingIndexNamePattern.FindStringSubmatch(name)
	if match == nil {
		return 0, 0, false
	}
	dimension, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0, false
	}
	version, err = strconv.Atoi(match[2])
	if err != nil {
		return 0, 0, false
	}
	return dimension, version, true
}
