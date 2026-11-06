package httpapi

import (
	"log/slog"

	"github.com/example/rag-platform/internal/config"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes composes every resource surface. cmd/api calls this once;
// route ownership is by resource, not by delivery milestone. Each handler gets
// only the service slice it owns.
func RegisterRoutes(router *gin.Engine, cfg config.Config, deps RouteDeps, logger *slog.Logger) {
	mw := MiddlewareDeps{Identity: deps.Identity, Authorization: deps.Authorization, Limiter: deps.Limiter, DB: deps.DB}
	api := router.Group("/api/v1")
	protected := api.Group("")
	protected.Use(authenticationMiddleware(cfg, mw))

	registerAuthRoutes(api, protected, cfg, AuthDeps{Identity: deps.Identity, Authorization: deps.Authorization}, mw, logger)
	RegisterKnowledgeBaseRoutes(protected, cfg, KnowledgeBaseDeps{KnowledgeBase: deps.KnowledgeBase}, mw, logger)
	RegisterKnowledgeBaseConfigRoutes(protected, cfg, KnowledgeBaseDeps{KnowledgeBase: deps.KnowledgeBase}, mw, logger)
	RegisterDocumentRoutes(protected, cfg, IngestionDeps{Ingestion: deps.Ingestion}, mw, logger)
	RegisterConversationRoutes(protected, cfg, ConversationDeps{Authorization: deps.Authorization, Conversation: deps.Conversation, Retrieval: deps.Retrieval}, mw, logger)
	RegisterConnectorRoutes(protected, cfg, DataSourceDeps{DataSources: deps.DataSources}, mw, logger)
	RegisterWikiProjectionRoutes(protected, cfg, ProjectionDeps{Projection: deps.Projection}, mw, logger)
	RegisterGraphProjectionRoutes(protected, cfg, ProjectionDeps{Projection: deps.Projection}, mw, logger)
	RegisterOperationsRoutes(protected, cfg, OperationsDeps{Ingestion: deps.Ingestion, Ops: deps.Ops, Queue: deps.Queue, Tasks: deps.Tasks, Metrics: deps.Metrics, Models: deps.Models}, mw, logger)
}
