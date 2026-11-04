package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/logging"
	"github.com/example/rag-platform/internal/platform/evidenceindex"
	"github.com/example/rag-platform/internal/platform/persistence"
	"github.com/example/rag-platform/internal/platform/vector"
	"gorm.io/gorm"
)

const usage = `usage: embedctl <command>

commands:
  create-index   create or rebuild the chunks HNSW index for EMBEDDING_DIMENSION
  reembed        re-embed chunks whose model or dimension no longer matches config
  verify         fail if any active chunk still holds a stale embedding

The embedding dimension is part of the index contract. Switching model or
dimension is a managed sequence: create-index, then reembed, then verify, and
only then enable retrieval on the new vectors.`

var commands = map[string]bool{"create-index": true, "reembed": true, "verify": true}

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
	db, err := persistence.Open(cfg.DatabaseURL, persistence.PoolOptions{
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
	case "create-index":
		recreated, err := vector.CreateHNSWIndex(ctx, db, cfg.Embedding.Dimension)
		if err != nil {
			fatal(logger, "create_index_failed", err)
		}
		logger.Info("vector_index_ready", "index", vector.ChunksIndexName, "dimension", cfg.Embedding.Dimension, "recreated", recreated)
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
