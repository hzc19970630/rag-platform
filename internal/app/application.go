package app

import (
	"context"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/contracts/metrics"
	"github.com/example/rag-platform/internal/contracts/projectioncache"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/example/rag-platform/internal/identity"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/example/rag-platform/internal/modelregistry"
	"github.com/example/rag-platform/internal/ops"
	"github.com/example/rag-platform/internal/platform/docreader"
	"github.com/example/rag-platform/internal/platform/evidenceindex"
	"github.com/example/rag-platform/internal/platform/evidencestore"
	"github.com/example/rag-platform/internal/platform/persistence"
	pgprojectioncache "github.com/example/rag-platform/internal/platform/projectioncache"
	"github.com/example/rag-platform/internal/platform/queue"
	"github.com/example/rag-platform/internal/platform/ratelimit"
	pgrelationstore "github.com/example/rag-platform/internal/platform/relationstore"
	"github.com/example/rag-platform/internal/platform/search"
	"github.com/example/rag-platform/internal/platform/secrets"
	"github.com/example/rag-platform/internal/platform/storage"
	"github.com/example/rag-platform/internal/platform/vector"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

type Application struct {
	DB            *gorm.DB
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
	Objects       *storage.S3Store
	Queue         *queue.Publisher
	Docreader     *docreader.Client
	Limiter       *ratelimit.Limiter
}

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
func Open(cfg config.Config, recorder metrics.Recorder) (*Application, error) {
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	database, err := persistence.Open(cfg.DatabaseURL, persistence.PoolOptions{
		MaxOpenConns:    cfg.DatabaseMaxOpenConns,
		MaxIdleConns:    cfg.DatabaseMaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.DatabaseConnMaxLifetimeSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	authorization, err := authz.NewService(database)
	if err != nil {
		return nil, fmt.Errorf("initialize authorization: %w", err)
	}
	if err := authorization.StartInvalidation(context.Background(), cfg.RedisAddr, cfg.RedisPassword); err != nil {
		closeDatabase(database)
		return nil, err
	}
	// Resolve the embedder before the knowledge-base service so a knowledge
	// base's binding is written from the adapter that will actually serve it.
	embedder, err := NewEmbedder(cfg)
	if err != nil {
		_ = authorization.Close()
		closeDatabase(database)
		return nil, err
	}
	identityService, err := identity.NewService(database, identityOptions(cfg.Auth))
	if err != nil {
		_ = authorization.Close()
		closeDatabase(database)
		return nil, fmt.Errorf("initialize identity: %w", err)
	}
	binding := knowledgebase.EmbeddingBinding{Model: embedder.Name(), Dimension: embedder.Dimension()}
	if _, err := knowledgebase.BackfillEmbeddingBinding(context.Background(), database, binding); err != nil {
		_ = authorization.Close()
		closeDatabase(database)
		return nil, err
	}
	knowledgeBaseService := knowledgebase.NewService(database, authorization, binding)
	objects, err := storage.NewS3(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Region, cfg.S3Bucket, cfg.S3UseTLS, cfg.S3ForcePathStyle, cfg.MaxFileBytes)
	if err != nil {
		closeDatabase(database)
		return nil, err
	}
	if err := objects.EnsureBucket(context.Background()); err != nil {
		closeDatabase(database)
		return nil, err
	}
	publisher := queue.NewPublisher(cfg.RedisAddr, cfg.RedisPassword)
	reader, err := docreader.New(cfg.DocreaderAddr)
	if err != nil {
		_ = publisher.Close()
		closeDatabase(database)
		return nil, err
	}
	indexWriter := evidenceindex.New(database)
	ingestionService := ingestion.NewService(database, authorization, objects, publisher, cfg.MaxFileBytes, cfg.Limits.DocumentRetentionDays, ingestion.Quota{MaxDocuments: cfg.Limits.TenantMaxDocuments, MaxStorageBytes: cfg.Limits.TenantMaxStorageBytes})
	ingestionService.SetIndexWriter(indexWriter)
	credentialCipher, err := secrets.NewKeyring(cfg.Connector.EncryptionKeys, cfg.Connector.EncryptionKey, cfg.Connector.ActiveKeyID)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeDatabase(database)
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
		closeDatabase(database)
		return nil, fmt.Errorf("load prompt templates: %w", err)
	}
	chatter, err := NewChatter(cfg)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeDatabase(database)
		return nil, err
	}
	reranker, err := NewReranker(cfg)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeDatabase(database)
		return nil, err
	}
	modelCatalog, err := newModelRegistry(database, cfg, chatter.Name(), reranker.Name(), embedder.Name(), probeOf(chatter), probeOf(reranker), probeOf(embedder))
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeDatabase(database)
		return nil, err
	}
	knowledgeBaseService.SetModelCatalog(modelCatalog)
	wikiGenerator, err := knowledgeprojection.NewGenerator(chatter, registry)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeDatabase(database)
		return nil, err
	}
	relationExtractor, err := knowledgeprojection.NewExtractor(chatter, registry)
	if err != nil {
		_ = reader.Close()
		_ = publisher.Close()
		closeDatabase(database)
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
		DB: database, Authz: authorization, Retrieval: retrievalService, Chat: chatter, Models: modelCatalog,
		Prompts: registry, Timeout: time.Duration(cfg.Chat.TimeoutSec) * time.Second,
		ChatRequestsPerDay: cfg.Limits.TenantChatRequestsPerDay,
		HistoryTurns:       cfg.Chat.HistoryTurns,
		HistoryTokenLimit:  cfg.Chat.HistoryTokenLimit,
		Metrics:            recorder,
	})
	return &Application{DB: database, Identity: identityService, Authorization: authorization, KnowledgeBase: knowledgeBaseService, Ingestion: ingestionService, Index: indexWriter, DataSources: dataSourceService, Projection: projectionService, Secrets: credentialCipher, Tasks: ingestion.NewTaskInspector(database, publisher), Retrieval: retrievalService, Conversation: conversationService, Models: modelCatalog, Prompts: registry, Ops: ops.NewService(database), Objects: objects, Queue: publisher, Docreader: reader, Limiter: ratelimit.New(cfg.Limits.RateLimitPerMinute)}, nil
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
	if a.DB == nil {
		return nil
	}
	sqlDB, err := a.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
