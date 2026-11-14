package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hzc19970630/rag-platform/internal/app"
	"github.com/hzc19970630/rag-platform/internal/buildinfo"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/httpapi"
	"github.com/hzc19970630/rag-platform/internal/logging"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String())
		return
	}
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	buildinfo.Log(logger)
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
	// One registry is shared by HTTP middleware and the application services so
	// the scrape endpoint and /ops/metrics report the same process counters.
	metricsRegistry := observability.New()
	application, err := app.Open(cfg, metricsRegistry, app.RoleAPI)
	if err != nil {
		logger.Error("application_bootstrap_failed", "error", err)
		os.Exit(1)
	}
	defer application.Close()
	// /readyz asks the live database and bucket rather than dialing TCP ports,
	// so it must be built from the opened application.
	r := httpapi.NewRouterWithReadiness(cfg, logger, metricsRegistry, application.Readiness)
	httpapi.RegisterRoutes(r, cfg, httpapi.RouteDeps{
		Identity: application.Identity, Authorization: application.Authorization, Limiter: application.Limiter,
		DB:            application.DB,
		KnowledgeBase: application.KnowledgeBase, Ingestion: application.Ingestion, Conversation: application.Conversation,
		Retrieval: application.Retrieval, DataSources: application.DataSources, Projection: application.Projection,
		Ops: application.Ops, Tenant: application.Tenant, TenantConfig: application.TenantConfig, ConfigHealth: application.ConfigHealth, ConfigPreview: application.ConfigPreview, Queue: application.Queue, Tasks: application.Tasks,
		Metrics: metricsRegistry, Models: application.Models, Embeddings: application.Embeddings, Quality: application.Quality,
	}, logger)

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
			Handler:           observability.HandlerFor(metricsRegistry),
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
