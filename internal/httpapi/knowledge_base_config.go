package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/transport/views"
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
		c.JSON(http.StatusOK, views.KnowledgeBaseConfiguration(view))
	})

	byID.PUT("/retrieval-config", func(c *gin.Context) {
		var input httpapi.RetrievalConfigOverride
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.ReplaceRetrievalConfig(c.Request.Context(), accessContext(c).Principal, c.Param("id"), views.ParamsOverride(input))
		if err != nil {
			writeError(c, err)
			return
		}
		view, err := knowledgebase.BuildKnowledgeBaseConfiguration(row, cfg.Retrieval, knowledgebase.ReadOnlySystemConfiguration(cfg))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.KnowledgeBaseConfiguration(view))
	})

}
