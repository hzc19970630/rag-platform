package httpapi

import (
	"log/slog"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes composes every resource surface. cmd/api calls this once;
// route ownership is by resource, not by delivery milestone.
func RegisterRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	RegisterAuthRoutes(router, cfg, application, logger)
	RegisterKnowledgeBaseRoutes(router, cfg, application, logger)
	RegisterKnowledgeBaseConfigRoutes(router, cfg, application, logger)
	RegisterDocumentRoutes(router, cfg, application, logger)
	RegisterConversationRoutes(router, cfg, application, logger)
	RegisterConnectorRoutes(router, cfg, application, logger)
	RegisterWikiProjectionRoutes(router, cfg, application, logger)
	RegisterGraphProjectionRoutes(router, cfg, application, logger)
	RegisterOperationsRoutes(router, cfg, application, logger)
}
