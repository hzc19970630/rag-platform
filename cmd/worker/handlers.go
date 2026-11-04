package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/example/rag-platform/internal/platform/queue"
	"github.com/hibiken/asynq"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// taskHandlers owns the mapping from a transport task type to the domain
// worker. Keeping it outside main lets the registry grow without duplicating
// decode/metric/terminal-error logic at the call site.
type taskHandlers struct {
	pipeline    *ingestion.PipelineWorker
	application *app.Application
	logger      *slog.Logger
}

func newTaskHandlers(pipeline *ingestion.PipelineWorker, application *app.Application, logger *slog.Logger) *taskHandlers {
	return &taskHandlers{pipeline: pipeline, application: application, logger: logger}
}

func (h *taskHandlers) register(mux *asynq.ServeMux) {
	mux.HandleFunc(queue.TaskTypeIngestion, h.ingest)
	mux.HandleFunc(queue.TaskTypeSync, h.sync)
	mux.HandleFunc(queue.TaskTypeWiki, h.wiki)
	mux.HandleFunc(queue.TaskTypeRelation, h.relation)
}

func (h *taskHandlers) ingest(ctx context.Context, task *asynq.Task) (err error) {
	var message ingestion.Task
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		return fmt.Errorf("decode ingestion task: %w", err)
	}
	ctx, span := observability.StartSpan(ctx, "ingestion.stage",
		attribute.String("stage", message.Stage),
		attribute.String("pipeline_run.id", message.PipelineRunID),
		attribute.String("document_version.id", message.DocumentVersionID),
		attribute.String("trace.id", message.TraceID),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	started := time.Now()
	err = h.pipeline.Process(ctx, message)
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
}

func (h *taskHandlers) sync(ctx context.Context, task *asynq.Task) (err error) {
	var message datasource.SyncTask
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		return fmt.Errorf("decode connector sync task: %w", err)
	}
	ctx, span := observability.StartSpan(ctx, "connector.sync",
		attribute.String("connector.id", message.ConnectorID),
		attribute.String("sync_run.id", message.SyncRunID),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	started := time.Now()
	err = h.application.DataSources.ProcessSync(ctx, message)
	result := "success"
	if err != nil {
		result = "failure"
	}
	observability.Default().Inc("connector_sync_attempt_total", observability.Labels("result", result))
	observability.Default().Observe("connector_sync_duration_ms", time.Since(started), observability.Labels("mode", message.Queue))
	return err
}

func (h *taskHandlers) wiki(ctx context.Context, task *asynq.Task) error {
	return h.projection(ctx, task, queue.TaskTypeWiki, "wiki_projection")
}

func (h *taskHandlers) relation(ctx context.Context, task *asynq.Task) error {
	return h.projection(ctx, task, queue.TaskTypeRelation, "relation_projection")
}

func (h *taskHandlers) projection(ctx context.Context, task *asynq.Task, decodeLabel, metricPrefix string) (err error) {
	var message knowledgeprojection.Task
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		return fmt.Errorf("decode %s task: %w", decodeLabel, err)
	}
	ctx, span := observability.StartSpan(ctx, metricPrefix+".stage",
		attribute.String("stage", message.Stage),
		attribute.String("projection_type", message.ProjectionType),
		attribute.String("projection_run.id", message.ProjectionRunID),
		attribute.String("evidence_package.id", message.EvidencePackageID),
	)
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	started := time.Now()
	err = h.application.Projection.Process(ctx, message)
	result := "success"
	if err != nil {
		result = "failure"
	}
	observability.Default().Inc(metricPrefix+"_stage_total", observability.Labels("stage", message.Stage, "result", result))
	observability.Default().Observe(metricPrefix+"_stage_duration_ms", time.Since(started), observability.Labels("stage", message.Stage))
	if err != nil && apperror.IsTerminal(err) {
		// Terminal projection failures are recorded on the run/stage rows and
		// must not retry. Evidence RAG is unaffected either way.
		return fmt.Errorf("%s: %w", err.Error(), asynq.SkipRetry)
	}
	return err
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
