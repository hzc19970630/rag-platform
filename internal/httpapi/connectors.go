package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/gin-gonic/gin"
)

func RegisterConnectorRoutes(protected *gin.RouterGroup, cfg config.Config, deps DataSourceDeps, mw MiddlewareDeps, logger *slog.Logger) {
	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.POST("/connectors", func(c *gin.Context) {
		var input datasource.CreateConnectorInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, err := deps.DataSources.Connectors().Create(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, view)
	})
	knowledgeBases.GET("/connectors", func(c *gin.Context) {
		views, err := deps.DataSources.Connectors().List(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"connectors": views})
	})
	knowledgeBases.POST("/sync/cancel", func(c *gin.Context) {
		cancelled, err := deps.DataSources.Runs().CancelKnowledgeBase(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"cancelled": cancelled})
	})

	connectors := protected.Group("/connectors/:id")
	connectors.GET("", func(c *gin.Context) {
		view, err := deps.DataSources.Connectors().Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	connectors.PATCH("", func(c *gin.Context) {
		var input datasource.UpdateConnectorInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, err := deps.DataSources.Connectors().Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
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
		c.JSON(http.StatusAccepted, run)
	})
	connectors.POST("/cancel", func(c *gin.Context) {
		cancelled, err := deps.DataSources.Runs().CancelConnector(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"cancelled": cancelled})
	})
	connectors.GET("/permissions", func(c *gin.Context) {
		view, err := deps.DataSources.Connectors().Permissions(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	connectors.GET("/runs", func(c *gin.Context) {
		runs, err := deps.DataSources.Runs().ListRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	})
	connectors.GET("/runs/:runId", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().GetRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, run)
	})
	connectors.POST("/runs/:runId/retry", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().RetryRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
	connectors.POST("/runs/:runId/cancel", func(c *gin.Context) {
		run, err := deps.DataSources.Runs().CancelRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
}
