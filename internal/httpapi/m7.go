package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/gin-gonic/gin"
)

// RegisterM7Routes wires the relation projection and graph query surface.
// Query and review views need knowledge-base read; rebuild and retry need write
// scope and reject API keys; snapshot review needs manage_members.
func RegisterM7Routes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.GET("/graph/query", func(c *gin.Context) {
		result, err := application.Projection.QueryGraph(c.Request.Context(), accessContext(c).Principal, knowledgeprojection.GraphQueryOptions{
			KnowledgeBaseID: c.Param("id"), Query: c.Query("q"),
			Depth: intQuery(c, "depth"), Limit: intQuery(c, "limit"),
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	knowledgeBases.GET("/graph/snapshots", func(c *gin.Context) {
		snapshots, err := application.Projection.ListGraphSnapshots(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"snapshots": snapshots})
	})
	knowledgeBases.GET("/graph/runs", func(c *gin.Context) {
		runs, err := application.Projection.ListRelationRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	})
	knowledgeBases.POST("/graph/rebuild", func(c *gin.Context) {
		count, err := application.Projection.RebuildGraph(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"started": count})
	})

	snapshots := protected.Group("/graph/snapshots/:snapshotId")
	snapshots.GET("", func(c *gin.Context) {
		detail, err := application.Projection.GetGraphSnapshot(c.Request.Context(), accessContext(c).Principal, c.Param("snapshotId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	})
	snapshots.POST("/review", func(c *gin.Context) {
		var input knowledgeprojection.GraphReviewInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		snapshot, err := application.Projection.ReviewGraphSnapshot(c.Request.Context(), accessContext(c).Principal, c.Param("snapshotId"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, snapshot)
	})

	runs := protected.Group("/graph/runs/:runId")
	runs.GET("", func(c *gin.Context) {
		detail, err := application.Projection.GetGraphRun(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	})
	runs.POST("/retry", func(c *gin.Context) {
		run, err := application.Projection.Retry(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
	_ = logger
}
