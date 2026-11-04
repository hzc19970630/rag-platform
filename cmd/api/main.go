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
	shutdownTracing, err := observability.InitTracing(context.Background(), cfg.OTELExporterEndpoint, cfg.OTELServiceName)
	if err != nil {
		logger.Error("tracing_bootstrap_failed", "component", "api", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(shutdownCtx)
	}()
	r := httpapi.NewRouterWithLogger(cfg, logger)
	application, err := app.Open(cfg)
	if err != nil {
		logger.Error("application_bootstrap_failed", "error", err)
		os.Exit(1)
	}
	defer application.Close()
	httpapi.RegisterRoutes(r, cfg, application, logger)

	// ReadHeaderTimeout blocks slowloris-style header trickling; ReadTimeout
	// bounds body reads without touching response writes, so it is compatible
	// with SSE; the five-minute window leaves room for a 100 MiB upload on a
	// slow link. IdleTimeout reclaims keep-alive connections. WriteTimeout stays
	// unset because SSE answers stream for the duration of generation.
	server := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
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
