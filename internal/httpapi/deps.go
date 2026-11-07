package httpapi

import (
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/example/rag-platform/internal/identity"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/example/rag-platform/internal/modelregistry"
	"github.com/example/rag-platform/internal/ops"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/example/rag-platform/internal/platform/queue"
	"github.com/example/rag-platform/internal/platform/ratelimit"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/example/rag-platform/internal/tenant"
	"gorm.io/gorm"
)

// Each resource handler receives only the services it calls. The transport
// package never imports the application container, so a handler cannot reach
// an unrelated business capability.
type AuthDeps struct {
	Identity      *identity.Service
	Authorization *authz.Service
}

type KnowledgeBaseDeps struct {
	KnowledgeBase *knowledgebase.Service
}

type IngestionDeps struct {
	Ingestion *ingestion.Service
}

type ConversationDeps struct {
	Authorization *authz.Service
	Conversation  *conversation.Service
	Retrieval     *retrieval.Service
}

type DataSourceDeps struct {
	DataSources *datasource.Service
}

type ProjectionDeps struct {
	Projection *knowledgeprojection.Service
}

type TenantDeps struct {
	Tenant *tenant.Service
}

type OperationsDeps struct {
	Ingestion *ingestion.Service
	Ops       *ops.Service
	Queue     *queue.Publisher
	Tasks     *ingestion.TaskInspector
	// Metrics is the process registry shared with the composition root.
	Metrics *observability.Registry
	// Models exposes provider health for degradation visibility.
	Models *modelregistry.Registry
}

type MiddlewareDeps struct {
	Identity      *identity.Service
	Authorization *authz.Service
	Limiter       *ratelimit.Limiter
	// DB is the runtime handle used to open the per-request tenant transaction.
	// Nil keeps the middleware single-statement behavior (tests, SQLite).
	DB *gorm.DB
}

// RouteDeps is the full wiring surface passed to RegisterRoutes. It is built
// in the composition root, not inside the transport package.
type RouteDeps struct {
	Identity      *identity.Service
	Authorization *authz.Service
	Limiter       *ratelimit.Limiter
	DB            *gorm.DB
	KnowledgeBase *knowledgebase.Service
	Ingestion     *ingestion.Service
	Conversation  *conversation.Service
	Retrieval     *retrieval.Service
	DataSources   *datasource.Service
	Projection    *knowledgeprojection.Service
	Ops           *ops.Service
	Tenant        *tenant.Service
	Queue         *queue.Publisher
	Tasks         *ingestion.TaskInspector
	Metrics       *observability.Registry
	Models        *modelregistry.Registry
}
