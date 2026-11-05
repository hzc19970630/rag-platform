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
	mw := MiddlewareDeps{Identity: deps.Identity, Authorization: deps.Authorization, Limiter: deps.Limiter}
	RegisterAuthRoutes(router, cfg, AuthDeps{Identity: deps.Identity, Authorization: deps.Authorization}, mw, logger)
	RegisterKnowledgeBaseRoutes(router, cfg, KnowledgeBaseDeps{KnowledgeBase: deps.KnowledgeBase}, mw, logger)
	RegisterKnowledgeBaseConfigRoutes(router, cfg, KnowledgeBaseDeps{KnowledgeBase: deps.KnowledgeBase}, mw, logger)
	RegisterDocumentRoutes(router, cfg, IngestionDeps{Ingestion: deps.Ingestion}, mw, logger)
	RegisterConversationRoutes(router, cfg, ConversationDeps{Authorization: deps.Authorization, Conversation: deps.Conversation, Retrieval: deps.Retrieval}, mw, logger)
	RegisterConnectorRoutes(router, cfg, DataSourceDeps{DataSources: deps.DataSources}, mw, logger)
	RegisterWikiProjectionRoutes(router, cfg, ProjectionDeps{Projection: deps.Projection}, mw, logger)
	RegisterGraphProjectionRoutes(router, cfg, ProjectionDeps{Projection: deps.Projection}, mw, logger)
	RegisterOperationsRoutes(router, cfg, OperationsDeps{Ingestion: deps.Ingestion, Ops: deps.Ops, Queue: deps.Queue, Tasks: deps.Tasks, Metrics: deps.Metrics, Models: deps.Models}, mw, logger)
}
