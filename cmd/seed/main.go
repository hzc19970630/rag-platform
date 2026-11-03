package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/logging"
)

const defaultSeedDir = "migrations/seeds"

func main() {
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	if err := cfg.Validate("seed"); err != nil {
		logger.Error("configuration_invalid", "component", "seed", "error", err)
		os.Exit(1)
	}

	database, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("seed_database_open_failed", "error", err)
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
		logger.Error("seed_database_ping_failed", "error", pingErr)
		os.Exit(1)
	}
	if err := seed(ctx, database, seedDir(), logger); err != nil {
		logger.Error("seed_failed", "error", err)
		os.Exit(1)
	}
}

func seed(ctx context.Context, database *sql.DB, directory string, logger *slog.Logger) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read seed directory: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	for _, filename := range files {
		version := strings.TrimSuffix(filename, filepath.Ext(filename))
		var exists bool
		if err := database.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM seed_runs WHERE version = $1)", version).Scan(&exists); err != nil {
			return fmt.Errorf("check seed %s: %w", version, err)
		}
		if exists {
			logger.Info("seed_skipped", "version", version)
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, filename))
		if err != nil {
			return fmt.Errorf("read seed %s: %w", version, err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin seed %s: %w", version, err)
		}
		if _, err := transaction.ExecContext(ctx, string(contents)); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("execute seed %s: %w", version, err)
		}
		if _, err := transaction.ExecContext(ctx, "INSERT INTO seed_runs (version) VALUES ($1)", version); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record seed %s: %w", version, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit seed %s: %w", version, err)
		}
		logger.Info("seed_applied", "version", version)
	}
	return nil
}

func seedDir() string {
	if value := os.Getenv("SEEDS_DIR"); value != "" {
		return value
	}
	return defaultSeedDir
}
