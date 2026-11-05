package vector

import (
	"context"
	"fmt"
	"syscall"

	"gorm.io/gorm"
)

// IndexStatus is one chunks vector index as reported by embedctl check.
type IndexStatus struct {
	Name       string
	Dimension  int
	Version    int
	Valid      bool
	Definition string
	SizeBytes  int64
}

// DimensionCount is one bucket of the stored-embedding dimension histogram.
type DimensionCount struct {
	Dimension int
	Count     int64
}

// CheckReport is the read-only state an operator needs before switching the
// embedding dimension: what pgvector supports, which indexes exist and whether
// any build failed, which dimensions are still stored, and how much space is in
// use.
type CheckReport struct {
	PgvectorVersion    string
	Indexes            []IndexStatus
	DimensionHistogram []DimensionCount
	DatabaseSizeBytes  int64
	ChunksHeapBytes    int64
	DiskFreeBytes      uint64
	// DiskFreeScope names the filesystem diskFree was measured on. embedctl
	// cannot see the PostgreSQL data volume from inside its own container, so
	// this is the tool container's root filesystem and must not be read as the
	// database volume's free space.
	DiskFreeScope string
}

// CheckChunksIndexes collects index diagnostics without modifying the catalog.
func CheckChunksIndexes(ctx context.Context, db *gorm.DB) (CheckReport, error) {
	var report CheckReport
	sqlDB, err := db.DB()
	if err != nil {
		return report, fmt.Errorf("database handle: %w", err)
	}
	connection, err := sqlDB.Conn(ctx)
	if err != nil {
		return report, fmt.Errorf("acquire check connection: %w", err)
	}
	session := &postgresIndexSession{connection: connection}
	defer connection.Close()

	version, err := session.PgvectorVersion(ctx)
	if err != nil {
		return report, err
	}
	report.PgvectorVersion = version
	records, err := session.ChunkIndexes(ctx)
	if err != nil {
		return report, err
	}
	for _, record := range records {
		status := IndexStatus{Name: record.Name, Valid: record.Valid, Definition: record.Definition, Version: -1}
		if dimension, parseErr := dimensionFromIndexDef(record.Definition); parseErr == nil {
			status.Dimension = dimension
		}
		if _, versionNumber, ok := parseChunksIndexName(record.Name); ok {
			status.Version = versionNumber
		}
		status.SizeBytes, _ = session.IndexSizeBytes(ctx, record.Name)
		report.Indexes = append(report.Indexes, status)
	}

	if err := sqlDB.QueryRowContext(ctx, "SELECT pg_database_size(current_database())").Scan(&report.DatabaseSizeBytes); err != nil {
		return report, fmt.Errorf("read database size: %w", err)
	}
	if err := sqlDB.QueryRowContext(ctx, "SELECT COALESCE(pg_relation_size(to_regclass('chunks')), 0)").Scan(&report.ChunksHeapBytes); err != nil {
		return report, fmt.Errorf("read chunks heap size: %w", err)
	}
	rows, err := sqlDB.QueryContext(ctx, "SELECT embedding_dimension, count(*) FROM chunks WHERE embedding IS NOT NULL GROUP BY embedding_dimension ORDER BY embedding_dimension")
	if err != nil {
		return report, fmt.Errorf("read embedding dimension histogram: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bucket DimensionCount
		if err := rows.Scan(&bucket.Dimension, &bucket.Count); err != nil {
			return report, fmt.Errorf("scan embedding dimension histogram: %w", err)
		}
		report.DimensionHistogram = append(report.DimensionHistogram, bucket)
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read embedding dimension histogram: %w", err)
	}

	if free, err := diskFreeBytes("/"); err == nil {
		report.DiskFreeBytes = free
		report.DiskFreeScope = "embedctl-container-root"
	}
	return report, nil
}

func diskFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
