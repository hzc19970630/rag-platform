package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/gin-gonic/gin"
)

func RegisterKnowledgeBaseConfigRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	byID := protected.Group("/knowledge-bases/:id")
	byID.GET("/configuration", func(c *gin.Context) {
		view, err := application.KnowledgeBase.Configuration(
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
		row, err := application.KnowledgeBase.ReplaceRetrievalConfig(c.Request.Context(), accessContext(c).Principal, c.Param("id"), override)
		if err != nil {
			writeError(c, err)
			return
		}
		view, err := knowledgebase.BuildKnowledgeBaseConfiguration(row, cfg.Retrieval, knowledgebase.ReadOnlySystemConfiguration(cfg))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
}
