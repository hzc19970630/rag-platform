package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/httpapi"
	"github.com/example/rag-platform/internal/logging"
)

func main() {
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	if err := cfg.Validate("api"); err != nil {
		logger.Error("configuration_invalid", "component", "api", "error", err)
		os.Exit(1)
	}
	r := httpapi.NewRouterWithLogger(cfg, logger)
	application, err := app.Open(cfg)
	if err != nil {
		logger.Error("application_bootstrap_failed", "error", err)
		os.Exit(1)
	}
	defer application.Close()
	httpapi.RegisterM1Routes(r, cfg, application, logger)
	httpapi.RegisterM2Routes(r, cfg, application, logger)
	httpapi.RegisterM3Routes(r, cfg, application, logger)
	httpapi.RegisterM4Routes(r, cfg, application, logger)
	httpapi.RegisterM5Routes(r, cfg, application, logger)
	httpapi.RegisterM6Routes(r, cfg, application, logger)
	httpapi.RegisterM7Routes(r, cfg, application, logger)
	httpapi.RegisterM9Routes(r, cfg, application, logger)

	server := &http.Server{Addr: cfg.APIAddr, Handler: r}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("api_listening", slog.String("address", cfg.APIAddr), slog.String("environment", cfg.AppEnv))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		logger.Error("api_stopped", "error", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	// In-flight requests get the configured drain window; after that the
	// listener closes and requests still running are cut off.
	logger.Info("api_shutdown_started", slog.Int("timeout_seconds", cfg.Limits.ShutdownTimeoutSeconds))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Limits.ShutdownTimeoutSeconds)*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api_shutdown_incomplete", "error", err)
		_ = server.Close()
		os.Exit(1)
	}
	logger.Info("api_shutdown_complete")
}
