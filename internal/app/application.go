package app

import (
	"context"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/datasource"
	"github.com/example/rag-platform/internal/identity"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/example/rag-platform/internal/ops"
	"github.com/example/rag-platform/internal/platform/docreader"
	"github.com/example/rag-platform/internal/platform/evidenceindex"
	"github.com/example/rag-platform/internal/platform/evidencestore"
	"github.com/example/rag-platform/internal/platform/persistence"
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
	Prompts       *prompts.Registry
	Ops           *ops.Service
	Objects       *storage.S3Store
	Queue         *queue.Publisher
	Docreader     *docreader.Client
	Limiter       *ratelimit.Limiter
}

func Open(cfg config.Config) (*Application, error) {
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
	identityService, err := identity.NewService(database, cfg.Auth)
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
	objects, err := storage.NewS3(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Region, cfg.S3Bucket, cfg.S3UseTLS, cfg.S3ForcePathStyle)
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
	credentialCipher, err := secrets.NewCipher(cfg.Connector.EncryptionKey)
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
	projectionService := knowledgeprojection.NewService(knowledgeprojection.Options{
		DB: database, Authz: authorization, Embedder: embedder, Generator: wikiGenerator, Extractor: relationExtractor,
		Publisher: publisher, Timeout: time.Duration(cfg.TaskTimeoutSeconds) * time.Second,
		Store:    pgrelationstore.New(database),
		Evidence: evidencestore.New(database),
	})
	knowledgeBaseService.SetProjectionLifecycle(projectionService)
	retrievalService := retrieval.NewService(retrieval.Options{
		DB: database, Keyword: search.NewBM25Searcher(database), Vector: vector.NewSearcher(database),
		Embedder: embedder, Expander: search.NewChunkExpander(database), Reranker: reranker,
		Authz: authorization, Defaults: cfg.Retrieval, Join: projectionService,
	})
	conversationService := conversation.NewService(conversation.Options{
		DB: database, Authz: authorization, Retrieval: retrievalService, Chat: chatter,
		Prompts: registry, Timeout: time.Duration(cfg.Chat.TimeoutSec) * time.Second,
		ChatRequestsPerDay: cfg.Limits.TenantChatRequestsPerDay,
	})
	return &Application{DB: database, Identity: identityService, Authorization: authorization, KnowledgeBase: knowledgeBaseService, Ingestion: ingestionService, Index: indexWriter, DataSources: dataSourceService, Projection: projectionService, Secrets: credentialCipher, Tasks: ingestion.NewTaskInspector(database, publisher), Retrieval: retrievalService, Conversation: conversationService, Prompts: registry, Ops: ops.NewService(database), Objects: objects, Queue: publisher, Docreader: reader, Limiter: ratelimit.New(cfg.Limits.RateLimitPerMinute)}, nil
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
