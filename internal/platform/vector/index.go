package vector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ChunksIndexName is the legacy, unversioned name of the HNSW index that serves
// chunk vector search. New indexes are named chunks_vector_idx_<dim>_v<N>, so a
// rebuild never has to drop the serving index before its replacement is valid.
const ChunksIndexName = "chunks_vector_idx"

const minPgvectorVersion = "0.7.0"

var (
	halfvecDimensionPattern = regexp.MustCompile(`halfvec\((\d+)\)`)
	// A halfvec column that already carries a typmod is rendered without the
	// redundant cast, so the partial predicate is the remaining dimension
	// source. Production chunks.embedding has no typmod, but the manager must
	// still read indexes created on a typed column.
	predicateDimensionPattern = regexp.MustCompile(`embedding_dimension\s*=\s*(\d+)`)
	chunksIndexNamePattern    = regexp.MustCompile(`^chunks_vector_idx_(\d+)_v(\d+)$`)
)

const pgvectorVersionQuery = "SELECT extversion FROM pg_extension WHERE extname = 'vector'"

const chunkIndexesQuery = `
SELECT c.relname, i.indisvalid, pg_get_indexdef(i.indexrelid)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'chunks'
  AND left(c.relname, length($1::text)) = $1::text
ORDER BY c.relname`

type indexRecord struct {
	Name       string
	Valid      bool
	Definition string
}

// IndexOptions controls an online index build. Zero timeouts mean "no limit",
// matching PostgreSQL's own defaults; the shipped configuration sets a lock
// timeout so a rebuild fails instead of waiting forever behind a long
// transaction.
type IndexOptions struct {
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	DryRun           bool
	// KeepDimensions names dimensions that still have searchable chunks. The
	// manager never drops their indexes, even when they differ from the
	// dimension being built, so a wrong config cannot take the live index away.
	KeepDimensions map[int]bool
}

// EnsureResult describes what an online index build did. In a dry run Created
// is false and Dropped lists the indexes that a real run would remove.
type EnsureResult struct {
	Dimension int
	Index     string
	Created   bool
	Planned   bool
	Dropped   []string
	SizeBytes int64
	Duration  time.Duration
}

// indexSession is the subset of a dedicated PostgreSQL connection used by the
// online build. It is an interface so version, catalog and failure paths can be
// exercised without a live database.
type indexSession interface {
	PgvectorVersion(ctx context.Context) (string, error)
	ChunkIndexes(ctx context.Context) ([]indexRecord, error)
	Configure(ctx context.Context, options IndexOptions) error
	CreateIndex(ctx context.Context, name string, dimension int) error
	DropIndex(ctx context.Context, name string) error
	IndexSizeBytes(ctx context.Context, name string) (int64, error)
}

// ChunksIndexDimension reports the vector dimension of the serving chunks HNSW
// index. The boolean is false when no valid index exists yet. When several
// valid indexes coexist (an interrupted swap), the newest versioned one wins
// over the legacy name.
func ChunksIndexDimension(ctx context.Context, db *gorm.DB) (int, bool, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return 0, false, fmt.Errorf("database handle: %w", err)
	}
	records, err := listChunksIndexes(ctx, sqlDB)
	if err != nil {
		return 0, false, err
	}
	dimension := 0
	found := false
	bestVersion := -1
	for _, record := range records {
		if !record.Valid {
			continue
		}
		parsed, err := dimensionFromIndexDef(record.Definition)
		if err != nil {
			return 0, false, err
		}
		_, version, versioned := parseChunksIndexName(record.Name)
		if !versioned {
			version = -1
		}
		if !found || version > bestVersion {
			dimension = parsed
			bestVersion = version
			found = true
		}
	}
	return dimension, found, nil
}

// EnsureChunksIndex builds the chunks HNSW index for dimension without
// blocking writes: the new index is created CONCURRENTLY under a versioned
// name, verified, and only then are older indexes dropped. PostgreSQL rejects
// CONCURRENTLY inside a transaction block, so this runs on a dedicated
// connection rather than through GORM.
func EnsureChunksIndex(ctx context.Context, db *gorm.DB, dimension int, options IndexOptions) (EnsureResult, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return EnsureResult{}, fmt.Errorf("database handle: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("acquire index build connection: %w", err)
	}
	session := &postgresIndexSession{connection: connection}
	dimensions, err := activeChunkDimensions(ctx, sqlDB)
	if err != nil {
		_ = connection.Close()
		return EnsureResult{}, err
	}
	options.KeepDimensions = dimensions
	defer func() {
		// Session settings outlive Conn.Close (the connection returns to the
		// pool), so reset them before handing it back.
		resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = session.reset(resetCtx)
		_ = connection.Close()
	}()
	return ensureIndex(ctx, session, dimension, options)
}

func ensureIndex(ctx context.Context, session indexSession, dimension int, options IndexOptions) (result EnsureResult, err error) {
	result.Dimension = dimension
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	if dimension <= 0 {
		return result, fmt.Errorf("vector dimension must be a positive integer, got %d", dimension)
	}
	// Session timeouts must be in place before the first catalog read:
	// pg_get_indexdef acquires a lock on chunks, so an already-running
	// ACCESS EXCLUSIVE transaction would otherwise block the build before the
	// lock timeout that is supposed to protect it was applied.
	version, err := session.PgvectorVersion(ctx)
	if err != nil {
		return result, err
	}
	if err := checkPgvectorVersion(version); err != nil {
		return result, err
	}
	// Dry runs also configure the session: the plan still reads pg_get_indexdef,
	// which blocks behind an ACCESS EXCLUSIVE transaction without a lock timeout.
	if err := session.Configure(ctx, options); err != nil {
		return result, err
	}
	records, err := session.ChunkIndexes(ctx)
	if err != nil {
		return result, err
	}

	if options.DryRun {
		for _, record := range records {
			if !record.Valid {
				continue
			}
			current, parseErr := dimensionFromIndexDef(record.Definition)
			if parseErr == nil && current == dimension {
				result.Index = record.Name
				return result, nil
			}
		}
		result.Index = nextChunksIndexName(records, dimension)
		result.Planned = true
		result.Dropped = droppableChunksIndexNames(records, "", dimension, options.KeepDimensions)
		return result, nil
	}

	for _, record := range records {
		if record.Valid {
			continue
		}
		if err := session.DropIndex(ctx, record.Name); err != nil {
			return result, fmt.Errorf("clean invalid chunks index %s: %w", record.Name, err)
		}
	}
	// The catalog changed if residues were removed, so re-read it before
	// choosing a version or deciding that the build is a no-op.
	records, err = session.ChunkIndexes(ctx)
	if err != nil {
		return result, err
	}
	for _, record := range records {
		if !record.Valid {
			continue
		}
		current, parseErr := dimensionFromIndexDef(record.Definition)
		if parseErr != nil {
			return result, parseErr
		}
		if current == dimension {
			result.Index = record.Name
			result.Dropped, err = pruneChunksIndexes(ctx, session, records, record.Name, dimension, options.KeepDimensions)
			if err != nil {
				return result, err
			}
			result.SizeBytes = indexSize(ctx, session, record.Name)
			return result, nil
		}
	}

	target := nextChunksIndexName(records, dimension)
	if err := session.CreateIndex(ctx, target, dimension); err != nil {
		// A failed CONCURRENTLY build leaves an INVALID index behind. Drop it
		// with a fresh context so the old serving index stays untouched.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		dropErr := session.DropIndex(cleanupCtx, target)
		if dropErr != nil {
			return result, fmt.Errorf("create chunks vector index at dimension %d: %w (cleanup %s: %v)", dimension, err, target, dropErr)
		}
		return result, fmt.Errorf("create chunks vector index at dimension %d: %w", dimension, err)
	}

	records, err = session.ChunkIndexes(ctx)
	if err != nil {
		return result, err
	}
	verified := false
	for _, record := range records {
		if record.Name != target {
			continue
		}
		current, parseErr := dimensionFromIndexDef(record.Definition)
		if parseErr != nil {
			return result, parseErr
		}
		if !record.Valid || current != dimension || !indexCoversDimension(record.Definition, dimension) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = session.DropIndex(cleanupCtx, target)
			return result, fmt.Errorf("index %s did not validate after build (valid=%t dimension=%d)", target, record.Valid, current)
		}
		verified = true
	}
	if !verified {
		return result, fmt.Errorf("index %s is missing from the catalog after build", target)
	}

	result.Index = target
	result.Created = true
	result.Dropped, err = pruneChunksIndexes(ctx, session, records, target, dimension, options.KeepDimensions)
	if err != nil {
		return result, err
	}
	result.SizeBytes = indexSize(ctx, session, target)
	return result, nil
}

// pruneChunksIndexes drops superseded indexes for the target dimension and
// indexes for dimensions that no longer have searchable chunks. An index for a
// different dimension that still has chunks is kept: during a managed switch
// the old dimension remains searchable until reembed finishes.
func pruneChunksIndexes(ctx context.Context, session indexSession, records []indexRecord, keep string, targetDimension int, keepDimensions map[int]bool) ([]string, error) {
	var dropped []string
	for _, record := range records {
		if !record.Valid || record.Name == keep {
			continue
		}
		dimension, err := dimensionFromIndexDef(record.Definition)
		if err != nil {
			return dropped, err
		}
		if dimension != targetDimension && keepDimensions[dimension] {
			continue
		}
		if err := session.DropIndex(ctx, record.Name); err != nil {
			return dropped, fmt.Errorf("drop superseded chunks index %s: %w", record.Name, err)
		}
		dropped = append(dropped, record.Name)
	}
	return dropped, nil
}

func droppableChunksIndexNames(records []indexRecord, keep string, targetDimension int, keepDimensions map[int]bool) []string {
	var names []string
	for _, record := range records {
		if !record.Valid || record.Name == keep {
			continue
		}
		dimension, err := dimensionFromIndexDef(record.Definition)
		if err != nil {
			continue
		}
		if dimension != targetDimension && keepDimensions[dimension] {
			continue
		}
		names = append(names, record.Name)
	}
	return names
}

// activeChunkDimensions is the guard input: dimensions that still have
// searchable child chunks. Their indexes must outlive an index build for a
// different dimension.
func activeChunkDimensions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT DISTINCT embedding_dimension FROM chunks WHERE embedding IS NOT NULL AND parent_id IS NOT NULL AND embedding_dimension > 0")
	if err != nil {
		return nil, fmt.Errorf("load active chunk dimensions: %w", err)
	}
	defer rows.Close()
	dimensions := make(map[int]bool)
	for rows.Next() {
		var dimension int
		if err := rows.Scan(&dimension); err != nil {
			return nil, fmt.Errorf("scan active chunk dimension: %w", err)
		}
		dimensions[dimension] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load active chunk dimensions: %w", err)
	}
	return dimensions, nil
}

func indexSize(ctx context.Context, session indexSession, name string) int64 {
	size, err := session.IndexSizeBytes(ctx, name)
	if err != nil {
		return 0
	}
	return size
}

func nextChunksIndexName(records []indexRecord, dimension int) string {
	version := 0
	for _, record := range records {
		if !record.Valid {
			continue
		}
		indexDimension, indexVersion, ok := parseChunksIndexName(record.Name)
		if !ok || indexDimension != dimension {
			continue
		}
		if indexVersion > version {
			version = indexVersion
		}
	}
	return fmt.Sprintf("%s_%d_v%d", ChunksIndexName, dimension, version+1)
}

func parseChunksIndexName(name string) (dimension int, version int, ok bool) {
	match := chunksIndexNamePattern.FindStringSubmatch(name)
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

func dimensionFromIndexDef(definition string) (int, error) {
	match := halfvecDimensionPattern.FindStringSubmatch(definition)
	if match == nil {
		match = predicateDimensionPattern.FindStringSubmatch(definition)
	}
	if match == nil {
		return 0, fmt.Errorf("cannot parse halfvec dimension from index definition %q", definition)
	}
	dimension, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, fmt.Errorf("parse halfvec dimension from index definition %q: %w", definition, err)
	}
	return dimension, nil
}

func indexCoversDimension(definition string, dimension int) bool {
	pattern := regexp.MustCompile(fmt.Sprintf(`embedding_dimension\s*=\s*%d(\D|$)`, dimension))
	return pattern.MatchString(definition)
}

func checkPgvectorVersion(raw string) error {
	parts := strings.SplitN(strings.TrimSpace(raw), ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("cannot parse pgvector version %q", raw)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("cannot parse pgvector version %q: %w", raw, err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("cannot parse pgvector version %q: %w", raw, err)
	}
	if major == 0 && minor < 7 {
		return fmt.Errorf("pgvector %s is too old: halfvec HNSW indexes require >= %s", raw, minPgvectorVersion)
	}
	return nil
}

func buildChunksIndexStatement(name string, dimension int) string {
	return fmt.Sprintf(
		"CREATE INDEX CONCURRENTLY %s ON chunks USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE embedding_dimension = %d",
		quoteIndexName(name), dimension, dimension,
	)
}

func quoteIndexName(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func listChunksIndexes(ctx context.Context, database *sql.DB) ([]indexRecord, error) {
	rows, err := database.QueryContext(ctx, chunkIndexesQuery, ChunksIndexName)
	if err != nil {
		return nil, fmt.Errorf("read chunks vector indexes: %w", err)
	}
	defer rows.Close()
	var records []indexRecord
	for rows.Next() {
		var record indexRecord
		if err := rows.Scan(&record.Name, &record.Valid, &record.Definition); err != nil {
			return nil, fmt.Errorf("scan chunks vector index: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type postgresIndexSession struct {
	connection *sql.Conn
}

func (s *postgresIndexSession) PgvectorVersion(ctx context.Context) (string, error) {
	var version string
	if err := s.connection.QueryRowContext(ctx, pgvectorVersionQuery).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("pgvector extension is not installed")
		}
		return "", fmt.Errorf("read pgvector version: %w", err)
	}
	return version, nil
}

func (s *postgresIndexSession) ChunkIndexes(ctx context.Context) ([]indexRecord, error) {
	rows, err := s.connection.QueryContext(ctx, chunkIndexesQuery, ChunksIndexName)
	if err != nil {
		return nil, fmt.Errorf("read chunks vector indexes: %w", err)
	}
	defer rows.Close()
	var records []indexRecord
	for rows.Next() {
		var record indexRecord
		if err := rows.Scan(&record.Name, &record.Valid, &record.Definition); err != nil {
			return nil, fmt.Errorf("scan chunks vector index: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *postgresIndexSession) Configure(ctx context.Context, options IndexOptions) error {
	for _, statement := range []string{
		fmt.Sprintf("SET lock_timeout = %d", options.LockTimeout.Milliseconds()),
		fmt.Sprintf("SET statement_timeout = %d", options.StatementTimeout.Milliseconds()),
	} {
		if _, err := s.connection.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure index build session: %w", err)
		}
	}
	return nil
}

func (s *postgresIndexSession) CreateIndex(ctx context.Context, name string, dimension int) error {
	if _, err := s.connection.ExecContext(ctx, buildChunksIndexStatement(name, dimension)); err != nil {
		return err
	}
	return nil
}

func (s *postgresIndexSession) DropIndex(ctx context.Context, name string) error {
	_, err := s.connection.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+quoteIndexName(name))
	return err
}

func (s *postgresIndexSession) IndexSizeBytes(ctx context.Context, name string) (int64, error) {
	var size int64
	err := s.connection.QueryRowContext(ctx,
		"SELECT pg_relation_size(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relname = $1",
		name,
	).Scan(&size)
	return size, err
}

func (s *postgresIndexSession) reset(ctx context.Context) error {
	for _, statement := range []string{"RESET lock_timeout", "RESET statement_timeout"} {
		if _, err := s.connection.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
