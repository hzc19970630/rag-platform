package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/httpapi"
	"github.com/example/rag-platform/internal/logging"
	"github.com/example/rag-platform/internal/platform/observability"
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

	// ReadHeaderTimeout blocks slowloris-style header trickling; IdleTimeout
	// reclaims keep-alive connections. WriteTimeout stays unset because SSE
	// answers stream for the duration of generation.
	server := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	var metricsServer *http.Server
	if metricsAddr := strings.TrimSpace(cfg.MetricsAddr); metricsAddr != "" {
		metricsServer = &http.Server{
			Addr:              metricsAddr,
			Handler:           observability.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			logger.Info("metrics_listening", slog.String("address", metricsAddr))
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// Monitoring is auxiliary: a busy or invalid metrics port must
				// not take down the API data plane. Scrapes will report the
				// target as down instead.
				logger.Error("metrics_listener_stopped", "error", err)
			}
		}()
	}

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
	if metricsServer != nil {
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("metrics_shutdown_incomplete", "error", err)
			_ = metricsServer.Close()
		}
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api_shutdown_incomplete", "error", err)
		_ = server.Close()
		os.Exit(1)
	}
	logger.Info("api_shutdown_complete")
}
