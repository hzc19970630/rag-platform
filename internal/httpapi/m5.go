package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/gin-gonic/gin"
)

// RegisterM5Routes wires external-source connectors. Connector management is a
// tenant-operator surface; reading a connector follows knowledge-base read
// scope, and every synced document is governed by the knowledge-base ACL.
func RegisterM5Routes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.POST("/connectors", func(c *gin.Context) {
		var input datasource.CreateConnectorInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, err := application.DataSources.Create(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, view)
	})
	knowledgeBases.GET("/connectors", func(c *gin.Context) {
		views, err := application.DataSources.List(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"connectors": views})
	})
	knowledgeBases.POST("/sync/cancel", func(c *gin.Context) {
		cancelled, err := application.DataSources.CancelKnowledgeBase(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"cancelled": cancelled})
	})

	connectors := protected.Group("/connectors/:id")
	connectors.GET("", func(c *gin.Context) {
		view, err := application.DataSources.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
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
		view, err := application.DataSources.Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	connectors.DELETE("", func(c *gin.Context) {
		if err := application.DataSources.Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	connectors.POST("/sync", func(c *gin.Context) {
		run, err := application.DataSources.TriggerSync(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
	connectors.POST("/cancel", func(c *gin.Context) {
		cancelled, err := application.DataSources.CancelConnector(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"cancelled": cancelled})
	})
	connectors.GET("/permissions", func(c *gin.Context) {
		view, err := application.DataSources.Permissions(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	connectors.GET("/runs", func(c *gin.Context) {
		runs, err := application.DataSources.ListRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	})
	connectors.GET("/runs/:runId", func(c *gin.Context) {
		run, err := application.DataSources.GetRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, run)
	})
	connectors.POST("/runs/:runId/retry", func(c *gin.Context) {
		run, err := application.DataSources.RetryRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
	connectors.POST("/runs/:runId/cancel", func(c *gin.Context) {
		run, err := application.DataSources.CancelRun(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
	_ = logger
}
