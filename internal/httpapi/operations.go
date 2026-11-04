package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/gin-gonic/gin"
)

func RegisterOperationsRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	ops := protected.Group("/ops")
	ops.Use(requireOperator(application))
	ops.GET("/tasks/:key", func(c *gin.Context) {
		inspection, err := application.Tasks.Inspect(c.Request.Context(), accessContext(c).Principal, c.Param("key"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, inspection)
	})
	ops.GET("/dead-letters", func(c *gin.Context) {
		rows, err := application.Tasks.ListDeadLetters(c.Request.Context(), accessContext(c).Principal, c.Query("status"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	ops.POST("/dead-letters/:id/retry", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			writeError(c, apperror.ErrNotFound)
			return
		}
		if err := application.Tasks.RetryDeadLetter(c.Request.Context(), accessContext(c).Principal, id); err != nil {
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
		if err := application.Tasks.DiscardDeadLetter(c.Request.Context(), accessContext(c).Principal, id); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	ops.POST("/pipelines/:id/cancel", func(c *gin.Context) {
		if err := application.Tasks.CancelPipeline(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
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
		report, err := application.Ingestion.ReconcileTenant(c.Request.Context(), accessContext(c).Principal.TenantID, staleAfter)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, report)
	})
	ops.GET("/metrics", func(c *gin.Context) {
		snapshot := observability.Default().Snapshot()
		if depth, err := application.Queue.QueueDepth(c.Request.Context()); err != nil {
			snapshot["queueError"] = err.Error()
		} else {
			snapshot["queues"] = depth
		}
		report, err := application.Ops.Snapshot(c.Request.Context(), accessContext(c).Principal.TenantID, time.Now().Add(-24*time.Hour), cfg.Limits.OutboxMaxAttempts)
		if err != nil {
			snapshot["operations"] = gin.H{"error": err.Error()}
		} else {
			snapshot["operations"] = report
		}
		c.JSON(http.StatusOK, snapshot)
	})
	ops.GET("/metrics/prometheus", func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := observability.Default().WritePrometheus(c.Writer); err != nil {
			writeError(c, err)
		}
	})
}
