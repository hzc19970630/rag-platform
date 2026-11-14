package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/quality"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

func RegisterOperationsRoutes(protected *gin.RouterGroup, cfg config.Config, deps OperationsDeps, mw MiddlewareDeps, logger *slog.Logger) {
	// The command injects the process registry. A handler-only test that omits
	// it gets an isolated registry rather than silently reading global state.
	registry := deps.Metrics
	if registry == nil {
		registry = observability.New()
	}

	ops := protected.Group("/ops")
	ops.Use(requireOperator(mw))
	ops.POST("/quality/eval-runs", func(c *gin.Context) {
		var input httpapi.RetrievalEvalRunRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		principal := accessContext(c).Principal
		run, err := deps.Quality.RecordEvalRun(c.Request.Context(), principal.TenantID, quality.EvalRunInput{
			Kind: input.Kind, KnowledgeBaseID: input.KnowledgeBaseID, Status: input.Status,
			ConfigHash: input.ConfigHash, Metrics: input.Metrics, Report: input.Report, CreatedBy: principal.SubjectID,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.RetrievalEvalRun(run))
	})
	ops.GET("/quality", func(c *gin.Context) {
		days := intQuery(c, "days")
		if days <= 0 {
			days = 30
		}
		if days > 365 {
			days = 365
		}
		since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		summary, err := deps.Quality.Summary(c.Request.Context(), accessContext(c).Principal.TenantID, c.Query("knowledgeBaseId"), since)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.RetrievalQualitySummary(summary))
	})
	ops.GET("/tasks/:key", func(c *gin.Context) {
		inspection, err := deps.Tasks.Inspect(c.Request.Context(), accessContext(c).Principal, c.Param("key"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TaskInspection(inspection))
	})
	ops.GET("/dead-letters", func(c *gin.Context) {
		rows, err := deps.Tasks.ListDeadLetters(c.Request.Context(), accessContext(c).Principal, c.Query("status"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.DeadLetterList{Items: views.TaskDeadLetters(rows)})
	})
	ops.POST("/dead-letters/:id/retry", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			writeError(c, apperror.ErrNotFound)
			return
		}
		if err := deps.Tasks.RetryDeadLetter(c.Request.Context(), accessContext(c).Principal, id); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/dead-letters/:id/discard", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			writeError(c, apperror.ErrNotFound)
			return
		}
		if err := deps.Tasks.DiscardDeadLetter(c.Request.Context(), accessContext(c).Principal, id); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/pipelines/:id/cancel", func(c *gin.Context) {
		if err := deps.Tasks.CancelPipeline(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/reconcile", func(c *gin.Context) {
		staleAfter := 5 * time.Minute
		if seconds := intQuery(c, "staleAfterSeconds"); seconds > 0 {
			staleAfter = time.Duration(seconds) * time.Second
		}
		report, err := deps.Ingestion.ReconcileTenant(c.Request.Context(), accessContext(c).Principal.TenantID, staleAfter)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ReconcileReport(report))
	})
	ops.GET("/metrics", func(c *gin.Context) {
		snapshot := registry.Snapshot()
		if depth, err := deps.Queue.QueueDepth(c.Request.Context()); err != nil {
			snapshot["queueError"] = err.Error()
		} else {
			snapshot["queues"] = depth
		}
		report, err := deps.Ops.Snapshot(c.Request.Context(), accessContext(c).Principal.TenantID, time.Now().Add(-24*time.Hour), cfg.Limits.OutboxMaxAttempts)
		if err != nil {
			snapshot["operations"] = gin.H{"error": err.Error()}
		} else {
			snapshot["operations"] = report
		}
		// Provider health is probed lazily and cached by the catalog, so this
		// stays a diagnostics view rather than a request-path dependency.
		if deps.Models != nil {
			snapshot["modelHealth"] = deps.Models.Health(c.Request.Context())
		}
		c.JSON(http.StatusOK, snapshot)
	})
	ops.GET("/metrics/prometheus", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := registry.WritePrometheus(c.Writer); err != nil {
			writeError(c, err)
		}
		if deps.Models != nil {
			if err := writeModelHealth(c.Writer, deps.Models.Health(c.Request.Context())); err != nil {
				writeError(c, err)
			}
		}
	})
}

// writeModelHealth appends one gauge per catalog model so a scrape can alert on
// a degraded provider without calling the JSON endpoint.
func writeModelHealth(writer io.Writer, statuses []modelregistry.HealthStatus) error {
	if len(statuses) == 0 {
		return nil
	}
	var builder strings.Builder
	builder.WriteString("# TYPE model_health gauge\n")
	for _, status := range statuses {
		value := 0
		if status.Healthy {
			value = 1
		}
		fmt.Fprintf(&builder, "model_health{model=\"%s\",provider=\"%s\",version=\"%s\"} %d\n",
			prometheusLabel(status.Model), prometheusLabel(status.Provider), prometheusLabel(status.Version), value)
	}
	_, err := io.WriteString(writer, builder.String())
	return err
}

func prometheusLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}
