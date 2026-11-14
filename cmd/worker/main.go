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

	"github.com/hibiken/asynq"
	"github.com/hzc19970630/rag-platform/internal/app"
	"github.com/hzc19970630/rag-platform/internal/buildinfo"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/datasource"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/logging"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
	"github.com/hzc19970630/rag-platform/internal/platform/queue"
)

func main() {
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	buildinfo.Log(logger)
	if err := cfg.Validate("worker"); err != nil {
		logger.Error("configuration_invalid", "component", "worker", "error", err)
		os.Exit(1)
	}
	shutdownTracing, err := observability.InitTracing(context.Background(), cfg.OTELExporterEndpoint, cfg.OTELServiceName)
	if err != nil {
		logger.Error("tracing_bootstrap_failed", "component", "worker", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(shutdownCtx)
	}()
	metricsRegistry := observability.New()
	application, err := app.Open(cfg, metricsRegistry, app.RoleWorker)
	if err != nil {
		logger.Error("application_bootstrap_failed", "component", "worker", "error", err)
		os.Exit(1)
	}
	defer application.Close()
	// The worker records ingestion, projection and connector metrics in this
	// process. Without a listener those counters never reach Prometheus, so the
	// same opt-in endpoint as the API is served here as well.
	var metricsServer *http.Server
	if metricsAddr := strings.TrimSpace(cfg.MetricsAddr); metricsAddr != "" {
		metricsServer = &http.Server{
			Addr:              metricsAddr,
			Handler:           observability.HandlerFor(metricsRegistry),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			logger.Info("metrics_listening", "component", "worker", slog.String("address", metricsAddr))
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// Monitoring is auxiliary: a busy metrics port must not stop
				// ingestion or projection work.
				logger.Error("metrics_listener_stopped", "component", "worker", "error", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Limits.ShutdownTimeoutSeconds)*time.Second)
			defer cancel()
			_ = metricsServer.Shutdown(shutdownCtx)
		}()
	}
	embedder, err := app.NewEmbedder(cfg)
	if err != nil {
		logger.Error("embedding_adapter_invalid", "component", "worker", "error", err)
		os.Exit(1)
	}
	logger.Info("embedding_adapter_ready", "source", cfg.Embedding.Source, "model", cfg.Embedding.Model, "dimension", cfg.Embedding.Dimension)
	chunker := ingestion.NewRuneChunker(ingestion.RuneChunkerOptions{Size: cfg.Chunk.Size, Overlap: cfg.Chunk.Overlap, Markers: cfg.Chunk.Markers, MinSize: cfg.Chunk.MinSize})
	logger.Info("chunking_policy_ready", "strategy", chunker.Strategy(), "size", cfg.Chunk.Size, "overlap", cfg.Chunk.Overlap, "min_size", cfg.Chunk.MinSize, "markers", len(cfg.Chunk.Markers))
	pipeline := ingestion.NewPipelineWorker(application.DB, application.Objects, application.Docreader, embedder, application.Index, chunker, time.Duration(cfg.TaskTimeoutSeconds)*time.Second)
	pipeline.SetChunkingResolver(app.TenantChunkingResolver(application.TenantConfig))
	pipeline.SetEmbeddingResolver(application.Embedding)
	pipeline.SetProjectionTrigger(application.Projection)

	rootCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	outboxContext, cancelOutbox := context.WithCancel(context.Background())
	defer cancelOutbox()
	go ingestion.NewOutboxPublisher(application.DB, application.Queue, cfg.Limits.OutboxMaxAttempts, cfg.Limits.OutboxBatchSize).Run(outboxContext)
	go datasource.NewOutboxPublisher(application.DB, application.Queue, cfg.Limits.OutboxMaxAttempts, cfg.Limits.OutboxBatchSize).Run(outboxContext)
	go knowledgeprojection.NewOutboxPublisher(application.DB, application.Queue, cfg.Limits.OutboxMaxAttempts, cfg.Limits.OutboxBatchSize).Run(outboxContext)
	go runMaintenance(rootCtx, application, cfg, logger, metricsRegistry)

	server := asynq.NewServer(asynq.RedisClientOpt{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}, asynq.Config{
		Concurrency: cfg.Limits.WorkerConcurrency,
		// Separate pools keep operator retries and maintenance work from waiting
		// behind a bulk ingestion backlog. The weights are relative.
		Queues: map[string]int{
			queue.QueueIngestion:   10,
			queue.QueueInteractive: 5,
			queue.QueueProjection:  5,
			queue.QueueMaintenance: 1,
		},
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, taskErr error) {
			if task.Type() == queue.TaskTypeSync {
				recordSyncFailure(logger, application, ctx, task, taskErr)
				return
			}
			if task.Type() == queue.TaskTypeAnswer {
				recordAnswerFailure(logger, application, ctx, task, taskErr)
				return
			}
			recordDeadLetter(logger, application, ctx, task, taskErr)
		}),
	})
	mux := asynq.NewServeMux()
	newTaskHandlers(pipeline, application, logger, metricsRegistry).register(mux)
	logger.Info("worker_bootstrap_ready", slog.String("redis", cfg.RedisAddr), slog.Int("concurrency", cfg.Limits.WorkerConcurrency))

	go func() {
		<-rootCtx.Done()
		shutdownTimeout := time.Duration(cfg.Limits.ShutdownTimeoutSeconds) * time.Second
		done := make(chan struct{})
		go func() {
			server.Shutdown()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shutdownTimeout):
			logger.Warn("worker_shutdown_timeout", "timeout_seconds", cfg.Limits.ShutdownTimeoutSeconds)
			server.Stop()
		}
	}()
	if err := server.Run(mux); err != nil {
		logger.Error("worker_stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("worker_shutting_down")
}

const (
	retentionPurgeInterval    = time.Hour
	retentionPurgeBatchSize   = 1000
	taskHistoryPurgeBatchSize = 200
	retentionPurgeMaxBatches  = 10
)

// purgeInBatches drains at most retentionPurgeMaxBatches batches so a backlog
// shrinks without one long transaction per table.
func purgeInBatches(ctx context.Context, before time.Time, batchSize int, purge func(context.Context, time.Time, int) (int64, error)) (int64, error) {
	var total int64
	for batch := 0; batch < retentionPurgeMaxBatches; batch++ {
		count, err := purge(ctx, before, batchSize)
		if err != nil {
			return total, err
		}
		total += count
		if count < int64(batchSize) {
			break
		}
	}
	return total, nil
}

// runMaintenance performs retention purge and reconciliation on the worker so
// the API request path never pays for it. One replica holds the advisory lock
// for its lifetime and runs every tick; the others skip. If the leader exits,
// PostgreSQL releases the session lock and the next replica takes over on its
// next tick.
func runMaintenance(ctx context.Context, application *app.Application, cfg config.Config, logger *slog.Logger, gauges metrics.Gauge) {
	interval := time.Duration(cfg.Limits.ReconcileIntervalSeconds) * time.Second
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	staleAfter := 5 * time.Minute
	if interval > staleAfter/2 {
		staleAfter = interval
	}
	runner := &maintenanceRunner{application: application, cfg: cfg, logger: logger, staleAfter: staleAfter, gauges: gauges}
	var lock *persistence.SessionLock
	defer func() { lock.Release() }()
	leader := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runner.collectGauges(ctx)
			if !leader {
				acquiredLock, acquired, err := persistence.TrySessionLock(ctx, application.DB, maintenanceAdvisoryLockKey)
				if err != nil {
					logger.Error("maintenance_lock_failed", "error", err)
					continue
				}
				if !acquired {
					logger.Info("maintenance_lock_skipped")
					continue
				}
				lock = acquiredLock
				leader = true
				logger.Info("maintenance_lock_acquired")
			}
			// A database restart closes the lock session and PostgreSQL releases
			// the lock with it; step down so the next tick re-elects a leader.
			if !lock.Healthy(ctx) {
				logger.Warn("maintenance_lock_lost")
				lock.Release()
				lock = nil
				leader = false
				continue
			}
			runner.tick(ctx)
		}
	}
}

// maintenanceAdvisoryLockKey is an arbitrary stable key ("rag_main") shared by
// worker replicas. It must differ from the migration lock.
const maintenanceAdvisoryLockKey int64 = 0x7261675F6D61696E

type maintenanceRunner struct {
	application *app.Application
	cfg         config.Config
	logger      *slog.Logger
	staleAfter  time.Duration
	gauges      metrics.Gauge

	lastAuditPurge       time.Time
	lastStreamEventPurge time.Time
	lastTaskHistoryPurge time.Time
	lastLDAPSync         time.Time
	projectionCursor     string
}

// collectGauges publishes deployment-wide backlog as gauges. It runs on every
// replica before leader election because a scrape may target any worker, and
// these values are identical from every process.
func (m *maintenanceRunner) collectGauges(ctx context.Context) {
	if m.gauges == nil {
		return
	}
	if depth, err := m.application.Queue.QueueDepth(ctx); err != nil {
		m.logger.Warn("metrics_queue_depth_failed", "error", err)
	} else {
		for queueName, states := range depth {
			for state, value := range states {
				m.gauges.SetGauge("queue_depth", metrics.Labels("queue", queueName, "state", state), float64(value))
			}
		}
	}
	snapshot, err := m.application.Ops.PlatformSnapshot(ctx, time.Now(), m.cfg.Limits.OutboxMaxAttempts)
	if err != nil {
		m.logger.Warn("metrics_platform_snapshot_failed", "error", err)
		return
	}
	m.gauges.SetGauge("outbox_backlog", nil, float64(snapshot.OutboxBacklog))
	m.gauges.SetGauge("inbox_expired_leases", nil, float64(snapshot.InboxExpiredLeases))
	m.gauges.SetGauge("projection_pending_runs", nil, float64(snapshot.ProjectionPendingRuns))
	m.gauges.SetGauge("projection_lag_seconds", nil, snapshot.ProjectionOldestPendingSeconds)
}

func (m *maintenanceRunner) tick(ctx context.Context) {
	application, cfg, logger := m.application, m.cfg, m.logger
	purged, err := application.Ingestion.PurgeExpired(ctx, 50)
	if err != nil {
		logger.Error("maintenance_purge_failed", "error", err)
	} else if purged > 0 {
		logger.Info("maintenance_purge_complete", "purged", purged)
	}
	if cfg.Limits.AuditRetentionDays > 0 && time.Since(m.lastAuditPurge) >= retentionPurgeInterval {
		m.lastAuditPurge = time.Now()
		cutoff := m.lastAuditPurge.Add(-time.Duration(cfg.Limits.AuditRetentionDays) * 24 * time.Hour)
		auditPurged, err := purgeInBatches(ctx, cutoff, retentionPurgeBatchSize, application.Authorization.PurgeAuditLogs)
		if err != nil {
			logger.Error("audit_purge_failed", "error", err)
		}
		if auditPurged > 0 {
			logger.Info("audit_purge_complete", "rows", auditPurged, "retention_days", cfg.Limits.AuditRetentionDays)
		}
	}
	if cfg.Limits.StreamEventRetentionDays > 0 && time.Since(m.lastStreamEventPurge) >= retentionPurgeInterval {
		m.lastStreamEventPurge = time.Now()
		cutoff := m.lastStreamEventPurge.Add(-time.Duration(cfg.Limits.StreamEventRetentionDays) * 24 * time.Hour)
		streamPurged, err := purgeInBatches(ctx, cutoff, retentionPurgeBatchSize, application.Conversation.PurgeStreamEvents)
		if err != nil {
			logger.Error("stream_event_purge_failed", "error", err)
		}
		if streamPurged > 0 {
			logger.Info("stream_event_purge_complete", "rows", streamPurged, "retention_days", cfg.Limits.StreamEventRetentionDays)
		}
	}
	if cfg.Limits.TaskHistoryRetentionDays > 0 && time.Since(m.lastTaskHistoryPurge) >= retentionPurgeInterval {
		m.lastTaskHistoryPurge = time.Now()
		cutoff := m.lastTaskHistoryPurge.Add(-time.Duration(cfg.Limits.TaskHistoryRetentionDays) * 24 * time.Hour)
		historyPurged, err := purgeInBatches(ctx, cutoff, taskHistoryPurgeBatchSize, application.Ingestion.PurgeTaskHistory)
		if err != nil {
			logger.Error("task_history_purge_failed", "error", err)
		}
		if historyPurged > 0 {
			logger.Info("task_history_purge_complete", "rows", historyPurged, "retention_days", cfg.Limits.TaskHistoryRetentionDays)
		}
	}
	triggered, err := application.DataSources.Maintenance().ScheduleDue(ctx, 20)
	if err != nil {
		logger.Error("connector_schedule_failed", "error", err)
	} else if triggered > 0 {
		logger.Info("connector_sync_scheduled", "runs", triggered)
	}
	expired, err := application.DataSources.Maintenance().ExpireStaleRuns(ctx, time.Now())
	if err != nil {
		logger.Error("connector_run_expiry_failed", "error", err)
	} else if expired > 0 {
		logger.Warn("connector_runs_expired", "runs", expired)
	}
	// An answer job whose worker disappeared must end with a terminal event,
	// otherwise the HTTP tail (and every reconnect) waits forever.
	if staleAnswers, err := application.Conversation.SweepStaleAnswers(ctx, time.Now(), 100); err != nil {
		logger.Error("answer_job_sweep_failed", "error", err)
	} else if staleAnswers > 0 {
		logger.Warn("answer_jobs_expired", "jobs", staleAnswers)
	}
	report, err := application.Ingestion.Reconcile(ctx, m.staleAfter)
	if err != nil {
		logger.Error("maintenance_reconcile_failed", "error", err)
		return
	}
	if scheduled, nextCursor, err := application.Projection.ScheduleMissing(ctx, 20, m.projectionCursor); err != nil {
		logger.Error("projection_schedule_failed", "error", err)
	} else {
		m.projectionCursor = nextCursor
		if scheduled > 0 {
			logger.Info("projection_scheduled", "runs", scheduled)
		}
	}
	if cfg.Auth.LDAP.Enabled && cfg.Auth.LDAP.SyncIntervalSeconds > 0 &&
		time.Since(m.lastLDAPSync) >= time.Duration(cfg.Auth.LDAP.SyncIntervalSeconds)*time.Second {
		m.lastLDAPSync = time.Now()
		if result, err := application.Identity.SyncLDAP(ctx); err != nil {
			logger.Error("ldap_sync_failed", "error", err)
		} else {
			logger.Info("ldap_sync_complete", "users", result.Users, "created", result.Created, "updated", result.Updated, "removed", result.Removed)
		}
	}
	if reconciled, err := application.Projection.Reconcile(ctx); err != nil {
		logger.Error("projection_reconcile_failed", "error", err)
	} else if reconciled > 0 {
		logger.Info("projection_reconciled", "rows_staled", reconciled)
	}
	if purged, err := application.Projection.PurgeGenerationCache(ctx, 500); err != nil {
		logger.Error("projection_cache_purge_failed", "error", err)
	} else if purged > 0 {
		logger.Info("projection_cache_purged", "rows", purged)
	}
	if report.DocumentsRepaired+report.VersionsRepaired+int64(report.StagesRequeued+report.PipelinesCancelled)+report.InboxesExpired+report.DeadLettersResolved > 0 {
		logger.Info("maintenance_reconcile_complete",
			"documents_repaired", report.DocumentsRepaired,
			"versions_repaired", report.VersionsRepaired,
			"stages_requeued", report.StagesRequeued,
			"pipelines_cancelled", report.PipelinesCancelled,
			"inboxes_expired", report.InboxesExpired,
			"dead_letters_resolved", report.DeadLettersResolved,
		)
	}
}
