package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/example/rag-platform/internal/logging"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/example/rag-platform/internal/platform/queue"
	"github.com/example/rag-platform/internal/platform/search"
	"github.com/hibiken/asynq"
)

func main() {
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	if err := cfg.Validate("worker"); err != nil {
		logger.Error("configuration_invalid", "component", "worker", "error", err)
		os.Exit(1)
	}
	application, err := app.Open(cfg)
	if err != nil {
		logger.Error("application_bootstrap_failed", "component", "worker", "error", err)
		os.Exit(1)
	}
	defer application.Close()
	embedder, err := app.NewEmbedder(cfg)
	if err != nil {
		logger.Error("embedding_adapter_invalid", "component", "worker", "error", err)
		os.Exit(1)
	}
	logger.Info("embedding_adapter_ready", "source", cfg.Embedding.Source, "model", cfg.Embedding.Model, "dimension", cfg.Embedding.Dimension)
	chunker := ingestion.NewRuneChunker(ingestion.RuneChunkerOptions{Size: cfg.Chunk.Size, Overlap: cfg.Chunk.Overlap, Markers: cfg.Chunk.Markers, MinSize: cfg.Chunk.MinSize})
	logger.Info("chunking_policy_ready", "strategy", chunker.Strategy(), "size", cfg.Chunk.Size, "overlap", cfg.Chunk.Overlap, "min_size", cfg.Chunk.MinSize, "markers", len(cfg.Chunk.Markers))
	pipeline := ingestion.NewPipelineWorker(application.DB, application.Objects, application.Docreader, embedder, search.NewBM25Indexer(application.DB), chunker, time.Duration(cfg.TaskTimeoutSeconds)*time.Second)
	pipeline.SetProjectionTrigger(application.Projection)

	rootCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	outboxContext, cancelOutbox := context.WithCancel(context.Background())
	defer cancelOutbox()
	go ingestion.NewOutboxPublisher(application.DB, application.Queue, cfg.Limits.OutboxMaxAttempts).Run(outboxContext)
	go datasource.NewOutboxPublisher(application.DB, application.Queue, cfg.Limits.OutboxMaxAttempts).Run(outboxContext)
	go knowledgeprojection.NewOutboxPublisher(application.DB, application.Queue, cfg.Limits.OutboxMaxAttempts).Run(outboxContext)
	go runMaintenance(rootCtx, application, cfg, logger)

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
			recordDeadLetter(logger, application, ctx, task, taskErr)
		}),
	})
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskTypeIngestion, func(ctx context.Context, task *asynq.Task) error {
		var message ingestion.Task
		if err := json.Unmarshal(task.Payload(), &message); err != nil {
			return fmt.Errorf("decode ingestion task: %w", err)
		}
		started := time.Now()
		err := pipeline.Process(ctx, message)
		result := "success"
		if err != nil {
			result = "failure"
		}
		observability.Default().Inc("ingestion_stage_total", observability.Labels("stage", message.Stage, "result", result))
		observability.Default().Observe("ingestion_stage_duration_ms", time.Since(started), observability.Labels("stage", message.Stage))
		if err != nil && message.Stage == ingestion.StageEmbed {
			observability.Default().Inc("model_errors_total", observability.Labels("model", "embedding", "stage", "embed"))
		}
		if err != nil && apperror.IsTerminal(err) {
			// Terminal failures are already recorded on the stage, version and
			// inbox. Retrying them five times only delays the operator signal.
			return fmt.Errorf("%s: %w", err.Error(), asynq.SkipRetry)
		}
		return err
	})
	mux.HandleFunc(queue.TaskTypeSync, func(ctx context.Context, task *asynq.Task) error {
		var message datasource.SyncTask
		if err := json.Unmarshal(task.Payload(), &message); err != nil {
			return fmt.Errorf("decode connector sync task: %w", err)
		}
		started := time.Now()
		err := application.DataSources.ProcessSync(ctx, message)
		result := "success"
		if err != nil {
			result = "failure"
		}
		observability.Default().Inc("connector_sync_attempt_total", observability.Labels("result", result))
		observability.Default().Observe("connector_sync_duration_ms", time.Since(started), observability.Labels("mode", message.Queue))
		return err
	})
	mux.HandleFunc(queue.TaskTypeWiki, func(ctx context.Context, task *asynq.Task) error {
		var message knowledgeprojection.Task
		if err := json.Unmarshal(task.Payload(), &message); err != nil {
			return fmt.Errorf("decode wiki projection task: %w", err)
		}
		started := time.Now()
		err := application.Projection.Process(ctx, message)
		result := "success"
		if err != nil {
			result = "failure"
		}
		observability.Default().Inc("wiki_projection_stage_total", observability.Labels("stage", message.Stage, "result", result))
		observability.Default().Observe("wiki_projection_stage_duration_ms", time.Since(started), observability.Labels("stage", message.Stage))
		if err != nil && apperror.IsTerminal(err) {
			// Terminal projection failures are recorded on the run/stage rows and
			// never affect the Evidence Index, so they must not retry.
			return fmt.Errorf("%s: %w", err.Error(), asynq.SkipRetry)
		}
		return err
	})
	mux.HandleFunc(queue.TaskTypeRelation, func(ctx context.Context, task *asynq.Task) error {
		var message knowledgeprojection.Task
		if err := json.Unmarshal(task.Payload(), &message); err != nil {
			return fmt.Errorf("decode relation projection task: %w", err)
		}
		started := time.Now()
		err := application.Projection.Process(ctx, message)
		result := "success"
		if err != nil {
			result = "failure"
		}
		observability.Default().Inc("relation_projection_stage_total", observability.Labels("stage", message.Stage, "result", result))
		observability.Default().Observe("relation_projection_stage_duration_ms", time.Since(started), observability.Labels("stage", message.Stage))
		if err != nil && apperror.IsTerminal(err) {
			// Terminal relation failures are recorded on the run/stage rows and
			// never affect Evidence RAG or the Wiki projection.
			return fmt.Errorf("%s: %w", err.Error(), asynq.SkipRetry)
		}
		return err
	})
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

// runMaintenance performs retention purge and reconciliation on the worker so
// the API request path never pays for it. The operations are idempotent, so a
// restart simply repeats the last pass.
func runMaintenance(ctx context.Context, application *app.Application, cfg config.Config, logger *slog.Logger) {
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purged, err := application.Ingestion.PurgeExpired(ctx, 50)
			if err != nil {
				logger.Error("maintenance_purge_failed", "error", err)
			} else if purged > 0 {
				logger.Info("maintenance_purge_complete", "purged", purged)
			}
			triggered, err := application.DataSources.ScheduleDue(ctx, 20)
			if err != nil {
				logger.Error("connector_schedule_failed", "error", err)
			} else if triggered > 0 {
				logger.Info("connector_sync_scheduled", "runs", triggered)
			}
			expired, err := application.DataSources.ExpireStaleRuns(ctx, time.Now())
			if err != nil {
				logger.Error("connector_run_expiry_failed", "error", err)
			} else if expired > 0 {
				logger.Warn("connector_runs_expired", "runs", expired)
			}
			report, err := application.Ingestion.Reconcile(ctx, staleAfter)
			if err != nil {
				logger.Error("maintenance_reconcile_failed", "error", err)
				continue
			}
			if scheduled, err := application.Projection.ScheduleMissing(ctx, 20); err != nil {
				logger.Error("projection_schedule_failed", "error", err)
			} else if scheduled > 0 {
				logger.Info("projection_scheduled", "runs", scheduled)
			}
			if reconciled, err := application.Projection.Reconcile(ctx); err != nil {
				logger.Error("projection_reconcile_failed", "error", err)
			} else if reconciled > 0 {
				logger.Info("projection_reconciled", "rows_staled", reconciled)
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
	}
}

// recordDeadLetter only fires when the retry budget is spent. A transient
// failure that still has retries left must not create operator noise.
func recordDeadLetter(logger *slog.Logger, application *app.Application, ctx context.Context, task *asynq.Task, taskErr error) {
	if errors.Is(taskErr, asynq.SkipRetry) {
		return
	}
	retried, hasRetried := asynq.GetRetryCount(ctx)
	maxRetry, hasMax := asynq.GetMaxRetry(ctx)
	if !hasRetried || !hasMax || retried < maxRetry {
		return
	}
	var message ingestion.Task
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		logger.Error("dead_letter_payload_invalid", "error", err)
		return
	}
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Tasks.RecordDeadLetter(recordCtx, message, taskErr); err != nil {
		logger.Error("dead_letter_record_failed", "idempotency_key", message.IdempotencyKey, "error", err)
		return
	}
	logger.Warn("dead_letter_recorded", "idempotency_key", message.IdempotencyKey, "stage", message.Stage)
}

// recordSyncFailure is the terminal hook for a connector sync task whose retry
// budget is spent. The durable run row is the source of truth, so retries that
// still have budget are ignored here.
func recordSyncFailure(logger *slog.Logger, application *app.Application, ctx context.Context, task *asynq.Task, taskErr error) {
	if errors.Is(taskErr, asynq.SkipRetry) {
		return
	}
	retried, hasRetried := asynq.GetRetryCount(ctx)
	maxRetry, hasMax := asynq.GetMaxRetry(ctx)
	if !hasRetried || !hasMax || retried < maxRetry {
		return
	}
	var message datasource.SyncTask
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		logger.Error("connector_sync_payload_invalid", "error", err)
		return
	}
	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.DataSources.FailRun(recordCtx, message, taskErr); err != nil {
		logger.Error("connector_sync_failure_record_failed", "run_id", message.SyncRunID, "error", err)
		return
	}
	logger.Warn("connector_sync_failed", "run_id", message.SyncRunID, "connector_id", message.ConnectorID, "error", taskErr)
}
