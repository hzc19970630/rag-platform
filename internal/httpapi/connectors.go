package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/transport/views"
	"github.com/gin-gonic/gin"
)

func RegisterConnectorRoutes(protected *gin.RouterGroup, cfg config.Config, deps DataSourceDeps, mw MiddlewareDeps, logger *slog.Logger) {
	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.POST("/connectors", func(c *gin.Context) {
		var input httpapi.ConnectorCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.DataSources.Connectors().Create(c.Request.Context(), accessContext(c).Principal, c.Param("id"), views.CreateConnectorInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.Connector(row))
	})
	knowledgeBases.GET("/connectors", func(c *gin.Context) {
		rows, err := deps.DataSources.Connectors().List(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.ConnectorList{Connectors: views.Connectors(rows)})
	})
	knowledgeBases.POST("/sync/cancel", func(c *gin.Context) {
		cancelled, err := deps.DataSources.Runs().CancelKnowledgeBase(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, httpapi.CancelResult{Cancelled: cancelled > 0})
	})

	connectors := protected.Group("/connectors/:id")
	connectors.GET("", func(c *gin.Context) {
		row, err := deps.DataSources.Connectors().Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.Connector(row))
	})
	connectors.PATCH("", func(c *gin.Context) {
		var input httpapi.ConnectorUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.DataSources.Connectors().Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), views.UpdateConnectorInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.Connector(row))
	})
	connectors.DELETE("", func(c *gin.Context) {
		if err := deps.DataSources.Connectors().Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	connectors.POST("/sync", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().TriggerSync(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.ConnectorRun(run))
	})
	connectors.POST("/cancel", func(c *gin.Context) {
		cancelled, err := deps.DataSources.Runs().CancelConnector(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, httpapi.CancelResult{Cancelled: cancelled > 0})
	})
	connectors.GET("/permissions", func(c *gin.Context) {
		view, err := deps.DataSources.Connectors().Permissions(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ConnectorPermissions(view))
	})
	connectors.GET("/runs", func(c *gin.Context) {
		runs, err := deps.DataSources.Runs().ListRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.ConnectorRunList{Runs: views.ConnectorRuns(runs)})
	})
	connectors.GET("/runs/:runId", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().GetRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ConnectorRun(run))
	})
	connectors.POST("/runs/:runId/retry", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().RetryRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.ConnectorRun(run))
	})
	connectors.POST("/runs/:runId/cancel", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().CancelRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.ConnectorRun(run))
	})
}
