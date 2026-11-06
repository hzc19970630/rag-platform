package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/gin-gonic/gin"
)

func RegisterKnowledgeBaseConfigRoutes(protected *gin.RouterGroup, cfg config.Config, deps KnowledgeBaseDeps, mw MiddlewareDeps, logger *slog.Logger) {
	byID := protected.Group("/knowledge-bases/:id")
	byID.GET("/configuration", func(c *gin.Context) {
		view, err := deps.KnowledgeBase.Configuration(
			c.Request.Context(),
			accessContext(c).Principal,
			c.Param("id"),
			cfg.Retrieval,
			knowledgebase.ReadOnlySystemConfiguration(cfg),
		)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})

	byID.PUT("/retrieval-config", func(c *gin.Context) {
		var override retrieval.ParamsOverride
		if err := c.ShouldBindJSON(&override); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.ReplaceRetrievalConfig(c.Request.Context(), accessContext(c).Principal, c.Param("id"), override)
		if err != nil {
			writeError(c, err)
			return
		}
		writeRetrievalConfigView(c, cfg, row)
	})

}

func writeRetrievalConfigView(c *gin.Context, cfg config.Config, row models.KnowledgeBase) {
	view, err := knowledgebase.BuildKnowledgeBaseConfiguration(row, cfg.Retrieval, knowledgebase.ReadOnlySystemConfiguration(cfg))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}
