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
	"github.com/hzc19970630/rag-platform/internal/contracts/vectorindex"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/logging"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
	"github.com/hzc19970630/rag-platform/internal/platform/vector"
	"gorm.io/gorm"
)

const usage = `usage: embedctl <command> [flags]

commands:
  check               report pgvector version, chunk vector indexes and stored dimensions
  create-index        build or replace the HNSW indexes online for a dimension
  backfill-embeddings copy legacy chunk vectors into the D3 variant table
  reembed             re-embed chunks whose model or dimension no longer matches config
  verify              fail if any active chunk still holds a stale embedding

create-index runs CREATE INDEX CONCURRENTLY under a versioned name and keeps
the serving index until the replacement is valid, so writes stay available.
It is a managed switch, not a routine command.

create-index flags:
  --dimension N  dimension to index (default EMBEDDING_DIMENSION)
  --dry-run      print the plan without changing the catalog
  --confirm      skip the confirmation prompt. Required when stdin is not a
                 terminal; EMBED_INDEX_CONFIRM=1 is equivalent.

create-index builds both the chunks index and the D3 chunk_embeddings index for
the dimension. It never drops another dimension's index.

The embedding dimension is part of the index contract. Switching model or
dimension is a managed sequence: create-index, then reembed, then verify, and
only then enable retrieval on the new vectors.`

var commands = map[string]bool{"check": true, "create-index": true, "backfill-embeddings": true, "reembed": true, "verify": true}

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
	indexManager := vector.NewManager(db)

	ctx := context.Background()
	switch command {
	case "check":
		checkIndex(ctx, indexManager, logger)
	case "create-index":
		createIndex(ctx, indexManager, cfg, logger, os.Args[2:])
	case "backfill-embeddings":
		total, err := ingestion.BackfillEmbeddingVariants(ctx, db, 1000)
		if err != nil {
			fatal(logger, "backfill_embeddings_failed", err)
		}
		logger.Info("backfill_embeddings_complete", "inserted", total)
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

func checkIndex(ctx context.Context, manager vectorindex.Manager, logger *slog.Logger) {
	report, err := manager.CheckChunksIndexes(ctx)
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
		"provider_version", report.ProviderVersion,
		"indexes", len(report.Indexes),
		"serving_index", serving,
		"invalid_indexes", invalid,
		"database_bytes", report.DatabaseSizeBytes,
		"heap_bytes", report.HeapBytes,
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
	variantReport, err := manager.CheckChunkEmbeddingIndexes(ctx)
	if err != nil {
		logger.Warn("chunk_embedding_index_check_failed", "error", err)
		return
	}
	for _, index := range variantReport.Indexes {
		logger.Info("chunk_embedding_index",
			"name", index.Name,
			"dimension", index.Dimension,
			"valid", index.Valid,
			"definition", index.Definition,
		)
	}
	for _, bucket := range variantReport.DimensionHistogram {
		logger.Info("chunk_embedding_dimension", "dimension", bucket.Dimension, "chunks", bucket.Count)
	}
}

func createIndex(ctx context.Context, manager vectorindex.Manager, cfg config.Config, logger *slog.Logger, args []string) {
	flags := flag.NewFlagSet("create-index", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dryRun := flags.Bool("dry-run", false, "print the online index plan without changing the catalog")
	confirm := flags.Bool("confirm", false, "skip the confirmation prompt")
	dimension := flags.Int("dimension", cfg.Embedding.Dimension, "vector dimension to index")
	if err := flags.Parse(args); err != nil {
		os.Exit(2)
	}
	if *dimension <= 0 {
		fatal(logger, "create_index_invalid_dimension", fmt.Errorf("dimension must be positive"))
	}
	if !*dryRun {
		if err := confirmIndexBuild(*confirm, *dimension); err != nil {
			fatal(logger, "create_index_not_confirmed", err)
		}
	}
	options := vectorindex.Options{
		LockTimeout:      time.Duration(cfg.Embedding.IndexLockTimeoutSec) * time.Second,
		StatementTimeout: time.Duration(cfg.Embedding.IndexStatementTimeoutSec) * time.Second,
		DryRun:           *dryRun,
	}
	result, err := manager.EnsureChunksIndex(ctx, *dimension, options)
	if err != nil {
		fatal(logger, "create_index_failed", err)
	}
	variant, err := manager.EnsureChunkEmbeddingIndex(ctx, *dimension, options)
	if err != nil {
		fatal(logger, "create_variant_index_failed", err)
	}
	if *dryRun {
		action := "none"
		if result.Planned {
			action = "create"
		}
		logger.Info("vector_index_plan",
			"index", result.Index,
			"variant_index", variant.Index,
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
		"variant_index", variant.Index,
		"dimension", result.Dimension,
		"created", result.Created,
		"variant_created", variant.Created,
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
