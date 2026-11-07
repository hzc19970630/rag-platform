package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/app"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/logging"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
	"github.com/hzc19970630/rag-platform/internal/platform/vector"
	"gorm.io/gorm"
)

const usage = `usage: embedctl <command> [flags]

commands:
  check          report pgvector version, chunk vector indexes and stored dimensions
  create-index   build or replace the chunks HNSW index online for EMBEDDING_DIMENSION
  reembed        re-embed chunks whose model or dimension no longer matches config
  verify         fail if any active chunk still holds a stale embedding

create-index runs CREATE INDEX CONCURRENTLY under a versioned name and keeps
the serving index until the replacement is valid, so writes stay available.
It is a managed switch, not a routine command.

create-index flags:
  --dry-run   print the plan without changing the catalog
  --confirm   skip the confirmation prompt. Required when stdin is not a
              terminal; EMBED_INDEX_CONFIRM=1 is equivalent.

The embedding dimension is part of the index contract. Switching model or
dimension is a managed sequence: create-index, then reembed, then verify, and
only then enable retrieval on the new vectors.`

var commands = map[string]bool{"check": true, "create-index": true, "reembed": true, "verify": true}

func main() {
	if len(os.Args) < 2 || !commands[os.Args[1]] {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	command := os.Args[1]
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	if err := cfg.Validate("embedctl"); err != nil {
		logger.Error("configuration_invalid", "component", "embedctl", "error", err)
		os.Exit(1)
	}
	db, err := persistence.Open(cfg.AdminDBURL(), persistence.PoolOptions{
		MaxOpenConns:    cfg.DatabaseMaxOpenConns,
		MaxIdleConns:    cfg.DatabaseMaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.DatabaseConnMaxLifetimeSeconds) * time.Second,
	})
	if err != nil {
		logger.Error("embedctl_database_open_failed", "error", err)
		os.Exit(1)
	}
	defer closeDatabase(db)

	ctx := context.Background()
	switch command {
	case "check":
		checkIndex(ctx, db, logger)
	case "create-index":
		createIndex(ctx, db, cfg, logger, os.Args[2:])
	case "reembed":
		embedder, err := app.NewEmbedder(cfg)
		if err != nil {
			fatal(logger, "embedding_adapter_invalid", err)
		}
		result, err := ingestion.RebuildEmbeddings(ctx, db, evidenceindex.New(db), embedder, cfg.Embedding.BatchSize)
		if err != nil {
			fatal(logger, "reembed_failed", err)
		}
		logger.Info("reembed_complete", "scanned", result.Scanned, "reembedded", result.Reembedded, "versions", result.Versions, "model", embedder.Name(), "dimension", embedder.Dimension())
	case "verify":
		embedder, err := app.NewEmbedder(cfg)
		if err != nil {
			fatal(logger, "embedding_adapter_invalid", err)
		}
		leftover, err := ingestion.VerifyEmbeddings(ctx, db, embedder.Name(), embedder.Dimension())
		if err != nil {
			fatal(logger, "verify_failed", err)
		}
		if leftover > 0 {
			logger.Error("embedding_verify_failed", "leftover", leftover, "model", embedder.Name(), "dimension", embedder.Dimension())
			os.Exit(1)
		}
		logger.Info("embedding_verify_ok", "model", embedder.Name(), "dimension", embedder.Dimension())
	}
}

func checkIndex(ctx context.Context, db *gorm.DB, logger *slog.Logger) {
	report, err := vector.CheckChunksIndexes(ctx, db)
	if err != nil {
		fatal(logger, "vector_index_check_failed", err)
	}
	serving := ""
	invalid := 0
	for _, index := range report.Indexes {
		if !index.Valid {
			invalid++
			continue
		}
		if serving == "" {
			serving = index.Name
		}
	}
	logger.Info("vector_index_check",
		"pgvector_version", report.PgvectorVersion,
		"indexes", len(report.Indexes),
		"serving_index", serving,
		"invalid_indexes", invalid,
		"database_bytes", report.DatabaseSizeBytes,
		"chunks_heap_bytes", report.ChunksHeapBytes,
		"disk_free_bytes", report.DiskFreeBytes,
		"disk_free_scope", report.DiskFreeScope,
	)
	for _, index := range report.Indexes {
		logger.Info("vector_index",
			"name", index.Name,
			"dimension", index.Dimension,
			"version", index.Version,
			"valid", index.Valid,
			"size_bytes", index.SizeBytes,
			"definition", index.Definition,
		)
	}
	for _, bucket := range report.DimensionHistogram {
		logger.Info("chunk_dimension", "dimension", bucket.Dimension, "chunks", bucket.Count)
	}
}

func createIndex(ctx context.Context, db *gorm.DB, cfg config.Config, logger *slog.Logger, args []string) {
	flags := flag.NewFlagSet("create-index", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dryRun := flags.Bool("dry-run", false, "print the online index plan without changing the catalog")
	confirm := flags.Bool("confirm", false, "skip the confirmation prompt")
	if err := flags.Parse(args); err != nil {
		os.Exit(2)
	}
	if !*dryRun {
		if err := confirmIndexBuild(*confirm, cfg.Embedding.Dimension); err != nil {
			fatal(logger, "create_index_not_confirmed", err)
		}
	}
	result, err := vector.EnsureChunksIndex(ctx, db, cfg.Embedding.Dimension, vector.IndexOptions{
		LockTimeout:      time.Duration(cfg.Embedding.IndexLockTimeoutSec) * time.Second,
		StatementTimeout: time.Duration(cfg.Embedding.IndexStatementTimeoutSec) * time.Second,
		DryRun:           *dryRun,
	})
	if err != nil {
		fatal(logger, "create_index_failed", err)
	}
	if *dryRun {
		action := "none"
		if result.Planned {
			action = "create"
		}
		logger.Info("vector_index_plan",
			"index", result.Index,
			"dimension", result.Dimension,
			"action", action,
			"drops", strings.Join(result.Dropped, ","),
			"lock_timeout_ms", (time.Duration(cfg.Embedding.IndexLockTimeoutSec) * time.Second).Milliseconds(),
			"statement_timeout_ms", (time.Duration(cfg.Embedding.IndexStatementTimeoutSec) * time.Second).Milliseconds(),
		)
		return
	}
	logger.Info("vector_index_ready",
		"index", result.Index,
		"dimension", result.Dimension,
		"created", result.Created,
		"dropped", strings.Join(result.Dropped, ","),
		"duration_ms", result.Duration.Milliseconds(),
		"size_bytes", result.SizeBytes,
		"lock_timeout_ms", (time.Duration(cfg.Embedding.IndexLockTimeoutSec) * time.Second).Milliseconds(),
		"statement_timeout_ms", (time.Duration(cfg.Embedding.IndexStatementTimeoutSec) * time.Second).Milliseconds(),
	)
}

func confirmIndexBuild(confirmed bool, dimension int) error {
	if confirmed || os.Getenv("EMBED_INDEX_CONFIRM") == "1" {
		return nil
	}
	// A non-interactive run reaches EOF here immediately and fails closed; it
	// must pass --confirm or EMBED_INDEX_CONFIRM=1 instead of relying on a TTY
	// check (docker run without -i still exposes a character device on stdin).
	fmt.Fprintf(os.Stderr, "Build the online chunks HNSW index for halfvec(%d)? [y/N] ", dimension)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("index build cancelled; pass --confirm or set EMBED_INDEX_CONFIRM=1 for non-interactive runs")
	}
}

func fatal(logger *slog.Logger, event string, err error) {
	logger.Error(event, "error", err)
	os.Exit(1)
}

func closeDatabase(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}
