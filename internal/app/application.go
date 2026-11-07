package app

import (
	"context"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/contracts/projectioncache"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/datasource"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgebase"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/ops"
	"github.com/hzc19970630/rag-platform/internal/platform/docreader"
	"github.com/hzc19970630/rag-platform/internal/platform/evidenceindex"
	"github.com/hzc19970630/rag-platform/internal/platform/evidencestore"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
	pgprojectioncache "github.com/hzc19970630/rag-platform/internal/platform/projectioncache"
	"github.com/hzc19970630/rag-platform/internal/platform/queue"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	pgrelationstore "github.com/hzc19970630/rag-platform/internal/platform/relationstore"
	"github.com/hzc19970630/rag-platform/internal/platform/search"
	"github.com/hzc19970630/rag-platform/internal/platform/secrets"
	"github.com/hzc19970630/rag-platform/internal/platform/storage"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"github.com/hzc19970630/rag-platform/internal/platform/vector"
	"github.com/hzc19970630/rag-platform/internal/prompts"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/tenant"
	"gorm.io/gorm"
)

type Application struct {
	DB *gorm.DB
	// MaintenanceDB bypasses row-level security for pre-tenant work: login and
	// API-key resolution in the API, and, in enforce mode, every worker job.
	// It is the same handle as DB unless the process runs with RLS enforced.
	MaintenanceDB *gorm.DB
	Identity      *identity.Service
	Authorization *authz.Service
	KnowledgeBase *knowledgebase.Service
	Ingestion     *ingestion.Service
	Index         ingestion.EvidenceIndexWriter
	DataSources   *datasource.Service
	Projection    *knowledgeprojection.Service
	Secrets       *secrets.Cipher
	Tasks         *ingestion.TaskInspector
	Retrieval     *retrieval.Service
	Conversation  *conversation.Service
	Models        *modelregistry.Registry
	Prompts       *prompts.Registry
	Ops           *ops.Service
	Tenant        *tenant.Service
	Objects       *storage.S3Store
	Queue         *queue.Publisher
	Docreader     *docreader.Client
	Limiter       *ratelimit.Limiter
}

// Role selects the process wiring. The API runs tenant requests on the
// non-bypass role and keeps a maintenance handle for authentication; the worker
// owns cross-tenant sweeps and runs entirely on the maintenance role.
type Role string

const (
	RoleAPI    Role = "api"
	RoleWorker Role = "worker"
)

// Readiness reports the live state of the database and the object store for
// /readyz. TCP reachability alone is not enough: a stopped PostgreSQL still
// accepts connections until it fails the handshake, and a missing bucket makes
// every upload fail while MinIO itself is healthy.
func (a *Application) Readiness(ctx context.Context) (string, string) {
	database := "unreachable"
	if a.DB != nil {
		if sqlDB, err := a.DB.DB(); err == nil {
			if err := sqlDB.PingContext(ctx); err == nil {
				database = "ok"
			}
		}
	}
	objects := "unreachable"
	if a.Objects != nil {
		if err := a.Objects.Ready(ctx); err == nil {
			objects = "ok"
		}
	}
	return database, objects
}

// Open builds the process-wide object graph. The metrics recorder is supplied
// by the command so the API, worker and the dedicated /metrics listener share
// one registry instead of reaching for a package-level default.
func Open(cfg config.Config, recorder metrics.Recorder, role Role) (*Application, error) {
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	if err := tenantdb.ConfigureRLSMode(cfg.TenantRLSMode); err != nil {
		return nil, err
	}
	pool := persistence.PoolOptions{
		MaxOpenConns:    cfg.DatabaseMaxOpenConns,
		MaxIdleConns:    cfg.DatabaseMaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.DatabaseConnMaxLifetimeSeconds) * time.Second,
	}
	enforced := tenantdb.CurrentRLSMode() == tenantdb.RLSModeEnforce
	runtimeURL := cfg.DatabaseURL
	if enforced && role == RoleWorker {
		// Worker jobs (outbox fanout, reconcile, retention) are cross-tenant by
		// design; strict policies would hide the rows they exist to process.
		runtimeURL = cfg.MaintenanceDatabaseURL
	}
	database, err := persistence.Open(runtimeURL, pool)
	if err != nil {
		return nil, err
	}
	maintenance := database
	closeAll := func() {
		closeDatabase(database)
		if maintenance != database {
			closeDatabase(maintenance)
		}
	}
	if enforced {
		if role == RoleAPI {
			if err := tenantdb.VerifyRuntimeRole(context.Background(), database); err != nil {
				closeAll()
				return nil, err
			}
			maintenance, err = persistence.Open(cfg.MaintenanceDatabaseURL, pool)
			if err != nil {
				closeAll()
				return nil, err
			}
			if err := tenantdb.VerifyMaintenanceRole(context.Background(), maintenance); err != nil {
				closeAll()
				return nil, err
			}
			// Redirect pooled API statements onto the per-request tenant
			// transaction. Enablement is still runtime-checked per request, so
			// the callbacks are inert until BeginRequestTx succeeds.
			tenantdb.InstallRequestTxCallbacks(database)
		} else if err := tenantdb.VerifyMaintenanceRole(context.Background(), database); err != nil {
			closeAll()
			return nil, err
		}
	}
	authorization, err := authz.NewService(authz.NewPostgresStores(database))
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("initialize authorization: %w", err)
	}
	if err := authorization.StartInvalidation(context.Background(), cfg.RedisAddr, cfg.RedisPassword); err != nil {
		closeAll()
		return nil, err
	}
	// Resolve the embedder before the knowledge-base service so a knowledge
	// base's binding is written from the adapter that will actually serve it.
	embedder, err := NewEmbedder(cfg)
	if err != nil {
		_ = authorization.Close()
		closeAll()
		return nil, err
	}
	identityService, err := identity.NewService(maintenance, identityOptions(cfg.Auth))
	if err != nil {
		_ = authorization.Close()
		closeAll()
		return nil, fmt.Errorf("initialize identity: %w", err)
	}
	binding := knowledgebase.EmbeddingBinding{Model: embedder.Name(), Dimension: embedder.Dimension()}
	if _, err := knowledgebase.BackfillEmbeddingBinding(context.Background(), maintenance, binding); err != nil {
		_ = authorization.Close()
		closeAll()
		return nil, err
	}
	knowledgeBaseService := knowledgebase.NewService(database, authorization, binding)
	objects, err := storage.NewS3(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Region, cfg.S3Bucket, cfg.S3UseTLS, cfg.S3ForcePathStyle, cfg.MaxFileBytes)
	if err != nil {
		closeAll()
		return nil, err
	}
	if err := objects.EnsureBucket(context.Background()); err != nil {
		closeAll()
		return nil, err
	}
	publisher := queue.NewPublisher(cfg.RedisAddr, cfg.RedisPassword)
	reader, err := docreader.New(cfg.DocreaderAddr)
	if err != nil {
		_ = publisher.Close()
		closeAll()
		return nil, err
	}
	indexWriter := evidenceindex.New(database)
	quotaResolver := tenantquota.NewResolver(database, tenantquota.Limits{
		MaxDocuments:       int64(cfg.Limits.TenantMaxDocuments),
		MaxStorageBytes:    cfg.Limits.TenantMaxStorageBytes,
		ChatRequestsPerDay: int64(cfg.Limits.TenantChatRequestsPerDay),
	})
	ingestionService := ingestion.NewService(database, authorization, objects, publisher, cfg.MaxFileBytes, cfg.Limits.DocumentRetentionDays, ingestion.Quota{MaxDocuments: int64(cfg.Limits.TenantMaxDocuments), MaxStorageBytes: cfg.Limits.TenantMaxStorageBytes})
	ingestionService.SetIndexWriter(indexWriter)
	ingestionService.SetQuotaResolver(quotaResolver)
	credentialCipher, err := secrets.NewKeyring(cfg.Connector.EncryptionKeys, cfg.Connector.EncryptionKey, cfg.Connector.ActiveKeyID)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, fmt.Errorf("initialize connector credentials: %w", err)
	}
	dataSourceService := datasource.NewService(database, authorization, credentialCipher, ingestionService, datasource.FetchOptions{
		AllowPrivateNetworks:     cfg.Connector.AllowPrivateNetworks,
		Timeout:                  time.Duration(cfg.Connector.RequestTimeoutSec) * time.Second,
		MaxManifestBytes:         cfg.Connector.MaxManifestBytes,
		MaxFileBytes:             cfg.MaxFileBytes,
		DefaultRequestsPerSecond: cfg.Connector.DefaultRatePerSecond,
	}, 0)

	// M3 online query path: prompt templates, the generation adapter, hybrid
	// retrieval and the conversation service.
	registry, err := prompts.Load(cfg.PromptTemplateDir)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, fmt.Errorf("load prompt templates: %w", err)
	}
	chatter, err := NewChatter(cfg)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, err
	}
	reranker, err := NewReranker(cfg)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, err
	}
	modelCatalog, err := newModelRegistry(database, cfg, chatter.Name(), reranker.Name(), embedder.Name(), probeOf(chatter), probeOf(reranker), probeOf(embedder))
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, err
	}
	knowledgeBaseService.SetModelCatalog(modelCatalog)
	wikiGenerator, err := knowledgeprojection.NewGenerator(chatter, registry)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, err
	}
	relationExtractor, err := knowledgeprojection.NewExtractor(chatter, registry)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeAll()
		return nil, err
	}
	var generationCache projectioncache.Cache
	if cfg.Projection.IncrementalEnabled {
		generationCache = pgprojectioncache.New(database)
	}
	projectionService := knowledgeprojection.NewService(knowledgeprojection.Options{
		DB: database, Authz: authorization, Embedder: embedder, Generator: wikiGenerator, Extractor: relationExtractor,
		Publisher: publisher, Timeout: time.Duration(cfg.TaskTimeoutSeconds) * time.Second,
		Store:    pgrelationstore.New(database),
		Evidence: evidencestore.New(database),
		Cache:    generationCache,
		Metrics:  recorder,
	})
	knowledgeBaseService.SetProjectionLifecycle(projectionService)
	var queryRewriter retrieval.Rewriter
	if cfg.Chat.QueryRewriteEnabled {
		queryRewriter = conversation.NewQueryRewriter(chatter, registry, time.Duration(cfg.Chat.QueryRewriteTimeoutSec)*time.Second, recorder)
	}
	retrievalService := retrieval.NewService(retrieval.Options{
		DB: database, Keyword: search.NewBM25Searcher(database), Vector: vector.NewSearcherWithMetrics(database, recorder),
		Embedder: embedder, Expander: search.NewChunkExpander(database), Reranker: reranker,
		Authz: authorization, Defaults: retrievalDefaults(cfg.Retrieval), Join: projectionService, Rewriter: queryRewriter, Metrics: recorder,
		RerankModels: modelCatalog,
	})
	conversationService := conversation.NewService(conversation.Options{
		Stores: conversation.NewPostgresStores(database), Authz: authorization, Retrieval: retrievalService, Chat: chatter, Models: modelCatalog,
		Prompts: registry, Timeout: time.Duration(cfg.Chat.TimeoutSec) * time.Second,
		ChatRequestsPerDay: cfg.Limits.TenantChatRequestsPerDay,
		QuotaResolver:      quotaResolver,
		HistoryTurns:       cfg.Chat.HistoryTurns,
		HistoryTokenLimit:  cfg.Chat.HistoryTokenLimit,
		Metrics:            recorder,
		AnswerPublisher:    publisher,
		AsyncEnabled:       cfg.Answer.AsyncEnabled,
		AnswerLease:        time.Duration(cfg.Answer.LeaseSeconds) * time.Second,
	})
	return &Application{DB: database, MaintenanceDB: maintenance, Identity: identityService, Authorization: authorization, KnowledgeBase: knowledgeBaseService, Ingestion: ingestionService, Index: indexWriter, DataSources: dataSourceService, Projection: projectionService, Secrets: credentialCipher, Tasks: ingestion.NewTaskInspector(database, publisher), Retrieval: retrievalService, Conversation: conversationService, Models: modelCatalog, Prompts: registry, Ops: ops.NewService(ops.NewPostgresStore(database)), Tenant: tenant.NewService(database, authorization, quotaResolver), Objects: objects, Queue: publisher, Docreader: reader, Limiter: ratelimit.New(cfg.Limits.RateLimitPerMinute)}, nil
}

func closeDatabase(database *gorm.DB) {
	if database == nil {
		return
	}
	sqlDB, err := database.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

func (a *Application) Close() error {
	if a == nil {
		return nil
	}
	if a.Docreader != nil {
		_ = a.Docreader.Close()
	}
	if a.Authorization != nil {
		_ = a.Authorization.Close()
	}
	if a.Queue != nil {
		_ = a.Queue.Close()
	}
	if a.MaintenanceDB != nil && a.MaintenanceDB != a.DB {
		closeDatabase(a.MaintenanceDB)
	}
	if a.DB == nil {
		return nil
	}
	sqlDB, err := a.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
