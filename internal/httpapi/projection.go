package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

func RegisterWikiProjectionRoutes(protected *gin.RouterGroup, cfg config.Config, deps ProjectionDeps, mw MiddlewareDeps, logger *slog.Logger) {
	knowledgeBases := protected.Group("/knowledge-bases/:id")
	knowledgeBases.GET("/projection", func(c *gin.Context) {
		status, err := deps.Projection.Status(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ProjectionState(status))
	})
	knowledgeBases.GET("/wiki/pages", func(c *gin.Context) {
		pages, err := deps.Projection.ListPages(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.WikiPageList{Pages: views.WikiPageSummaries(pages)})
	})
	knowledgeBases.GET("/wiki/runs", func(c *gin.Context) {
		runs, err := deps.Projection.ListRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.WikiRunList{Runs: views.ProjectionRuns(runs)})
	})
	knowledgeBases.POST("/wiki/rebuild", func(c *gin.Context) {
		count, err := deps.Projection.Rebuild(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, httpapi.StartResult{Started: count})
	})
	knowledgeBases.GET("/knowledge/search", func(c *gin.Context) {
		result, err := deps.Projection.Search(c.Request.Context(), accessContext(c).Principal, knowledgeprojection.SearchOptions{
			KnowledgeBaseID: c.Param("id"), Query: c.Query("q"), Limit: intQuery(c, "limit"),
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.KnowledgeSearchResult(result))
	})

	runs := protected.Group("/wiki/runs/:runId")
	runs.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetRun(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.WikiRunDetail(detail))
	})
	runs.POST("/retry", func(c *gin.Context) {
		run, err := deps.Projection.Retry(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.ProjectionRun(run))
	})

	pages := protected.Group("/wiki/pages/:pageId")
	pages.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetPage(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.WikiPageDetail(detail))
	})
	pages.GET("/versions", func(c *gin.Context) {
		versions, err := deps.Projection.ListVersions(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.WikiVersionList{Versions: views.WikiPageVersions(versions)})
	})
	pages.POST("/versions/:versionId/review", func(c *gin.Context) {
		var input httpapi.WikiReviewRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		version, err := deps.Projection.Review(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"), c.Param("versionId"), views.ReviewInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.WikiPageVersion(version))
	})
	pages.POST("/versions/:versionId/publish", func(c *gin.Context) {
		page, err := deps.Projection.Publish(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"), c.Param("versionId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.WikiPage(page))
	})
	pages.POST("/rollback", func(c *gin.Context) {
		var input httpapi.WikiRollbackRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		page, err := deps.Projection.Rollback(c.Request.Context(), accessContext(c).Principal, c.Param("pageId"), views.RollbackInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.WikiPage(page))
	})
}

func RegisterGraphProjectionRoutes(protected *gin.RouterGroup, cfg config.Config, deps ProjectionDeps, mw MiddlewareDeps, logger *slog.Logger) {
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
		c.JSON(http.StatusOK, views.GraphQueryResult(result))
	})
	knowledgeBases.GET("/graph/snapshots", func(c *gin.Context) {
		snapshots, err := deps.Projection.ListGraphSnapshots(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.GraphSnapshotList{Snapshots: views.GraphSnapshots(snapshots)})
	})
	knowledgeBases.GET("/graph/runs", func(c *gin.Context) {
		runs, err := deps.Projection.ListRelationRuns(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.GraphRunList{Runs: views.ProjectionRuns(runs)})
	})
	knowledgeBases.POST("/graph/rebuild", func(c *gin.Context) {
		count, err := deps.Projection.RebuildGraph(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, httpapi.StartResult{Started: count})
	})

	snapshots := protected.Group("/graph/snapshots/:snapshotId")
	snapshots.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetGraphSnapshot(c.Request.Context(), accessContext(c).Principal, c.Param("snapshotId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.GraphSnapshotDetail(detail))
	})
	snapshots.POST("/review", func(c *gin.Context) {
		var input httpapi.GraphReviewRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		snapshot, err := deps.Projection.ReviewGraphSnapshot(c.Request.Context(), accessContext(c).Principal, c.Param("snapshotId"), views.GraphReviewInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.GraphSnapshot(snapshot))
	})

	runs := protected.Group("/graph/runs/:runId")
	runs.GET("", func(c *gin.Context) {
		detail, err := deps.Projection.GetGraphRun(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.GraphRunDetail(detail))
	})
	runs.POST("/retry", func(c *gin.Context) {
		run, err := deps.Projection.Retry(c.Request.Context(), accessContext(c).Principal, c.Param("runId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.ProjectionRun(run))
	})
}
