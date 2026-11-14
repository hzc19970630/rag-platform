package httpapi

import (
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/confighealth"
	"github.com/hzc19970630/rag-platform/internal/configpreview"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/datasource"
	"github.com/hzc19970630/rag-platform/internal/embeddingmigration"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgebase"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/ops"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/platform/queue"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	"github.com/hzc19970630/rag-platform/internal/quality"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/tenant"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
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
	Quality       *quality.Service
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

// ConfigurationDeps owns the tenant business configuration surface (the
// deployment tier lives in env/YAML and is read-only to the API).
type ConfigurationDeps struct {
	TenantConfig *tenantconfig.Service
	Health       *confighealth.Service
	Preview      *configpreview.Service
}

// ModelDeps owns the tenant model catalog: provider CRUD and the merged
// read-only view used by knowledge-base settings.
type ModelDeps struct {
	Models *modelregistry.Registry
}

type OperationsDeps struct {
	Ingestion *ingestion.Service
	Ops       *ops.Service
	Queue     *queue.Publisher
	Tasks     *ingestion.TaskInspector
	// Metrics is the process registry shared with the composition root.
	Metrics *observability.Registry
	// Models exposes provider health for degradation visibility.
	Models  *modelregistry.Registry
	Quality *quality.Service
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
	TenantConfig  *tenantconfig.Service
	ConfigHealth  *confighealth.Service
	ConfigPreview *configpreview.Service
	Queue         *queue.Publisher
	Tasks         *ingestion.TaskInspector
	Metrics       *observability.Registry
	Models        *modelregistry.Registry
	Embeddings    *embeddingmigration.Service
	Quality       *quality.Service
}
