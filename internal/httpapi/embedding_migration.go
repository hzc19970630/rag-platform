package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/embeddingmigration"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// EmbeddingMigrationDeps owns the managed vector-space move. Starting and
// changing a migration are knowledge-base writes enforced by the service.
type EmbeddingMigrationDeps struct {
	Embeddings *embeddingmigration.Service
}

// RegisterEmbeddingMigrationRoutes exposes start, status and the explicit
// cutover/rollback/finalize actions. There is deliberately no "save and switch"
// endpoint: building and switching are separate operator decisions.
func RegisterEmbeddingMigrationRoutes(protected *gin.RouterGroup, deps EmbeddingMigrationDeps, mw MiddlewareDeps) {
	group := protected.Group("/knowledge-bases/:id/embedding-migration")
	group.POST("", func(c *gin.Context) {
		var input httpapi.EmbeddingMigrationStartRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		job, err := deps.Embeddings.Start(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input.TargetModel)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.EmbeddingMigration(job))
	})
	group.GET("", func(c *gin.Context) {
		job, err := deps.Embeddings.Latest(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.EmbeddingMigration(job))
	})
	group.POST("/:migrationId/cancel", func(c *gin.Context) {
		job, err := deps.Embeddings.Cancel(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("migrationId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.EmbeddingMigration(job))
	})
	group.POST("/:migrationId/cutover", func(c *gin.Context) {
		job, err := deps.Embeddings.Cutover(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("migrationId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.EmbeddingMigration(job))
	})
	group.POST("/:migrationId/rollback", func(c *gin.Context) {
		job, err := deps.Embeddings.Rollback(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("migrationId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.EmbeddingMigration(job))
	})
	group.POST("/:migrationId/finalize", func(c *gin.Context) {
		job, err := deps.Embeddings.Finalize(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("migrationId"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.EmbeddingMigration(job))
	})
}
