package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/logging"
)

const defaultMigrationDir = "migrations/postgres"

// migrationAdvisoryLockKey is an arbitrary stable key ("rag_mig") shared by
// every migrator process. PostgreSQL scopes advisory locks to the database.
const migrationAdvisoryLockKey int64 = 0x7261675F6D6967

func main() {
	checkOnly := flag.Bool("check", false, "verify applied migrations match the SQL files without applying anything")
	downVersion := flag.String("down", "", "roll back one applied migration using <version>.down.sql")
	flag.Parse()
	if *checkOnly && *downVersion != "" {
		fmt.Fprintln(os.Stderr, "migrate: -check and -down are mutually exclusive")
		os.Exit(1)
	}

	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	if err := cfg.Validate("migrate"); err != nil {
		logger.Error("configuration_invalid", "component", "migrate", "error", err)
		os.Exit(1)
	}

	database, err := sql.Open("pgx", cfg.AdminDBURL())
	if err != nil {
		logger.Error("migration_database_open_failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	ctx := context.Background()
	var pingErr error
	for attempt := 1; attempt <= 30; attempt++ {
		if pingErr = database.PingContext(ctx); pingErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if pingErr != nil {
		logger.Error("migration_database_ping_failed", "error", pingErr)
		os.Exit(1)
	}
	if *downVersion != "" {
		if err := rollbackMigration(ctx, database, migrationDir(), *downVersion, logger); err != nil {
			logger.Error("migration_rollback_failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := runMigrations(ctx, database, migrationDir(), logger, *checkOnly); err != nil {
		logger.Error("migration_failed", "error", err)
		os.Exit(1)
	}
	if *checkOnly {
		logger.Info("migrations_compatible")
	}
}

// rollbackMigration applies one <version>.down.sql file and removes its
// schema_migrations row in the same transaction. Only migrations that ship a
// down file can be rolled back; the historical set intentionally has none.
func rollbackMigration(ctx context.Context, database *sql.DB, directory, version string, logger *slog.Logger) error {
	if version == "" || filepath.Base(version) != version {
		return fmt.Errorf("invalid migration version %q", version)
	}
	releaseLock, err := lockMigrations(ctx, database, logger)
	if err != nil {
		return err
	}
	defer releaseLock()

	if _, err := database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version VARCHAR(128) PRIMARY KEY,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied := false
	if err := database.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&applied); err != nil {
		return fmt.Errorf("check applied migration %s: %w", version, err)
	}
	if !applied {
		return fmt.Errorf("migration %s is not applied", version)
	}
	path := filepath.Join(directory, version+".down.sql")
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read down migration %s: %w", version, err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollback %s: %w", version, err)
	}
	if _, err := transaction.ExecContext(ctx, string(sqlBytes)); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("execute rollback %s: %w", version, err)
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = $1", version); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("record rollback %s: %w", version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit rollback %s: %w", version, err)
	}
	logger.Info("migration_rolled_back", "version", version)
	return nil
}

type migrationFile struct {
	version  string
	path     string
	checksum string
}

// lockMigrations takes a session-level advisory lock on a dedicated
// connection. It must not use the pool: advisory locks belong to one session,
// and the pool could hand later statements to a different connection, which
// would leave the lock held by an idle session instead of the migration.
func lockMigrations(ctx context.Context, database *sql.DB, logger *slog.Logger) (func(), error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockKey); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	logger.Info("migration_lock_acquired")
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Closing the session releases the lock too, so a failed explicit
		// unlock cannot leak it past this process.
		_, _ = connection.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockKey)
		_ = connection.Close()
	}, nil
}

// runMigrations applies pending files and records a sha256 of each. In check
// mode it only verifies that every local file is applied and unmodified, which
// is what a deployment gate needs before rolling new code onto an old schema.
// A session-level advisory lock serializes concurrent migrators: without it two
// replicas can both decide the same file is pending and race on schema_migrations.
func runMigrations(ctx context.Context, database *sql.DB, directory string, logger *slog.Logger, checkOnly bool) error {
	releaseLock, err := lockMigrations(ctx, database, logger)
	if err != nil {
		return err
	}
	defer releaseLock()

	if _, err := database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version VARCHAR(128) PRIMARY KEY,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum VARCHAR(64)`); err != nil {
		return fmt.Errorf("add schema_migrations.checksum: %w", err)
	}

	files, err := readMigrationFiles(directory)
	if err != nil {
		return err
	}

	applied := map[string]string{}
	rows, err := database.QueryContext(ctx, "SELECT version, COALESCE(checksum, '') FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("list applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list applied migrations: %w", err)
	}

	migrated := map[string]bool{}
	for _, file := range files {
		migrated[file.version] = true
		stored, exists := applied[file.version]
		if !exists {
			if checkOnly {
				return fmt.Errorf("migration %s is not applied", file.version)
			}
			if err := applyMigration(ctx, database, file); err != nil {
				return err
			}
			logger.Info("migration_applied", "version", file.version)
			continue
		}
		if stored == "" {
			// Rows applied before checksum tracking are backfilled once; from then
			// on any edit to an applied file is reported as drift.
			if !checkOnly {
				if _, err := database.ExecContext(ctx, "UPDATE schema_migrations SET checksum = $1 WHERE version = $2", file.checksum, file.version); err != nil {
					return fmt.Errorf("backfill checksum for %s: %w", file.version, err)
				}
				logger.Info("migration_checksum_recorded", "version", file.version)
			}
			continue
		}
		if stored != file.checksum {
			return fmt.Errorf("migration %s checksum mismatch: applied %s, file %s", file.version, stored, file.checksum)
		}
		logger.Info("migration_skipped", "version", file.version)
	}

	if checkOnly {
		for version := range applied {
			if !migrated[version] {
				return fmt.Errorf("migration %s is applied but missing from %s", version, directory)
			}
		}
	}
	return nil
}

func applyMigration(ctx context.Context, database *sql.DB, file migrationFile) error {
	sqlBytes, err := os.ReadFile(file.path)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", file.version, err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", file.version, err)
	}
	if _, err := transaction.ExecContext(ctx, string(sqlBytes)); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("execute migration %s: %w", file.version, err)
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)", file.version, file.checksum); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("record migration %s: %w", file.version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", file.version, err)
	}
	return nil
}

func readMigrationFiles(directory string) ([]migrationFile, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || strings.HasSuffix(entry.Name(), ".down.sql") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(content)
		files = append(files, migrationFile{
			version:  strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			path:     path,
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}

func migrationDir() string {
	if value := os.Getenv("MIGRATIONS_DIR"); value != "" {
		return value
	}
	return defaultMigrationDir
}
