package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/gin-gonic/gin"
)

func RegisterWikiProjectionRoutes(router *gin.Engine, cfg config.Config, deps ProjectionDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, mw))

	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.GET("/projection", func(c *gin.Context) {
		status, err := deps.Projection.Status(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, status)
	})
	knowledgeBases.GET("/wiki/pages", func(c *gin.Context) {
		pages, err := deps.Projection.ListPages(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"pages": pages})
	})
	knowledgeBases.GET("/wiki/runs", func(c *gin.Context) {
		runs, err := deps.Projection.ListRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	})
	knowledgeBases.POST("/wiki/rebuild", func(c *gin.Context) {
		count, err := deps.Projection.Rebuild(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"started": count})
	})
	knowledgeBases.GET("/knowledge/search", func(c *gin.Context) {
		result, err := deps.Projection.Search(c.Request.Context(), accessContext(c).Principal, knowledgeprojection.SearchOptions{
			KnowledgeBaseID: c.Param("id"), Query: c.Query("q"), Limit: intQuery(c, "limit"),
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})

	runs := protected.Group("/wiki/runs/:runId")
	runs.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetRun(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	})
	runs.POST("/retry", func(c *gin.Context) {
		run, err := deps.Projection.Retry(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})

	pages := protected.Group("/wiki/pages/:pageId")
	pages.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetPage(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	})
	pages.GET("/versions", func(c *gin.Context) {
		versions, err := deps.Projection.ListVersions(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"versions": versions})
	})
	pages.POST("/versions/:versionId/review", func(c *gin.Context) {
		var input knowledgeprojection.ReviewInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		version, err := deps.Projection.Review(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"), c.Param("versionId"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, version)
	})
	pages.POST("/versions/:versionId/publish", func(c *gin.Context) {
		page, err := deps.Projection.Publish(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"), c.Param("versionId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	pages.POST("/rollback", func(c *gin.Context) {
		var input knowledgeprojection.RollbackInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		page, err := deps.Projection.Rollback(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
}

func RegisterGraphProjectionRoutes(router *gin.Engine, cfg config.Config, deps ProjectionDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, mw))

	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.GET("/graph/query", func(c *gin.Context) {
		result, err := deps.Projection.QueryGraph(c.Request.Context(), accessContext(c).Principal, knowledgeprojection.GraphQueryOptions{
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
		snapshots, err := deps.Projection.ListGraphSnapshots(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"snapshots": snapshots})
	})
	knowledgeBases.GET("/graph/runs", func(c *gin.Context) {
		runs, err := deps.Projection.ListRelationRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"runs": runs})
	})
	knowledgeBases.POST("/graph/rebuild", func(c *gin.Context) {
		count, err := deps.Projection.RebuildGraph(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"started": count})
	})

	snapshots := protected.Group("/graph/snapshots/:snapshotId")
	snapshots.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetGraphSnapshot(c.Request.Context(), accessContext(c).Principal, c.Param("snapshotId"))
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
		snapshot, err := deps.Projection.ReviewGraphSnapshot(c.Request.Context(), accessContext(c).Principal, c.Param("snapshotId"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, snapshot)
	})

	runs := protected.Group("/graph/runs/:runId")
	runs.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetGraphRun(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	})
	runs.POST("/retry", func(c *gin.Context) {
		run, err := deps.Projection.Retry(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, run)
	})
}
