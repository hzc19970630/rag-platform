package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/confighealth"
	"github.com/hzc19970630/rag-platform/internal/configpreview"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/datasource"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgebase"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/ops"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	"github.com/hzc19970630/rag-platform/internal/platform/secrets"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type httpProjectionAuthorizer struct{}

func (httpProjectionAuthorizer) Authorize(context.Context, authz.Principal, string, string) (bool, error) {
	return true, nil
}
func (httpProjectionAuthorizer) AuthorizeKnowledgeBase(context.Context, authz.Principal, string, string) (bool, error) {
	return true, nil
}
func (httpProjectionAuthorizer) BuildScope(_ context.Context, principal authz.Principal, target authz.QueryTarget) (authz.AuthorizationScope, error) {
	return authz.AuthorizationScope{SubjectID: principal.SubjectID, TenantID: principal.TenantID, KnowledgeBaseIDs: []string{target.KnowledgeBaseID}, Capabilities: []string{"knowledge_base:read"}}, nil
}
func (httpProjectionAuthorizer) FilterReadable(_ context.Context, _ authz.AuthorizationScope, refs []authz.ResourceRef) ([]authz.ResourceRef, error) {
	return refs, nil
}

type httpOpsStore struct{}

func (httpOpsStore) Snapshot(context.Context, string, time.Time, int) (ops.Snapshot, error) {
	return ops.Snapshot{OutboxBacklog: 2}, nil
}
func (httpOpsStore) PlatformSnapshot(context.Context, time.Time, int) (ops.PlatformSnapshot, error) {
	return ops.PlatformSnapshot{}, nil
}

func newProjectionHTTPRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.Membership{},
		&models.AuthorizationRevision{}, &models.AuditLog{},
		&models.Document{}, &models.DocumentVersion{}, &models.Chunk{},
		&models.WikiPage{}, &models.WikiPageVersion{}, &models.WikiPageSource{},
		&models.KnowledgeIndexRecord{}, &models.ProjectionRun{}, &models.ProjectionStageRun{},
		&models.KnowledgeProjectionState{}, &models.EvidencePackage{}, &models.TaskInbox{}, &models.TaskDeadLetter{},
		&models.PipelineRun{}, &models.StageRun{}, &models.OutboxEvent{}, &models.SyncRun{}, &models.AnswerJob{}, &models.Message{}, &models.Tenant{}, &models.Connector{}, &models.ConnectorItem{}, &models.Conversation{}, &models.Message{}, &models.MessageCitation{}, &models.StreamEvent{}, &models.ModelProvider{}, &models.ModelCatalogEntry{},
	); err != nil {
		t.Fatalf("migrate http projection tables: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&models.Tenant{ID: "tenant-a", Name: "Tenant A", Quota: "{}", RetrievalConfig: "{}", ChunkingConfig: "{}", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner", CreatedAt: now, UpdatedAt: now},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer", CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB", ACLVersion: 1,
		IndexingStrategy: `{"vector_enabled":true,"keyword_enabled":true,"graph_enabled":false,"wiki_enabled":true,"required_capabilities":["evidence"]}`, RetrievalConfig: "{}", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	if err := db.Create(&models.Document{
		ID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "owner-a",
		Filename: "a.md", MimeType: "text/markdown", Status: ingestion.StatusEvidenceReady,
		LatestVersionID: "ver-a", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-a", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Version: 1,
		ContentHash: "hash", SizeBytes: 1, Status: ingestion.StatusEvidenceReady, ACLVersion: 1,
		SourceObjectKey: "src-a", NormalizedKey: "norm-a", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.Create(&models.Chunk{
		ID: "chunk-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-a",
		ParentID: "parent-a", Ordinal: 0, Content: "chunk", ContentHash: "chunk-hash", Status: ingestion.StatusEvidenceReady,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-b", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Version: 2,
		ContentHash: "hash-b", SizeBytes: 2, Status: ingestion.StatusEvidenceReady, ACLVersion: 1,
		SourceObjectKey: "src-b", NormalizedKey: "norm-a", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed second version: %v", err)
	}
	for _, pipeline := range []models.PipelineRun{
		{ID: "pipe-a", PipelineType: ingestion.PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-a", IdempotencyKey: "pipe-a", Status: ingestion.StatusEvidenceReady, CreatedAt: now, UpdatedAt: now},
		{ID: "pipe-b", PipelineType: ingestion.PipelineTypeEvidence, TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentID: "doc-a", DocumentVersionID: "ver-b", IdempotencyKey: "pipe-b", Status: ingestion.StatusEvidenceReady, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)},
	} {
		if err := db.Create(&pipeline).Error; err != nil {
			t.Fatalf("seed pipeline: %v", err)
		}
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: "pipe-a", Stage: ingestion.StageParse, Status: ingestion.StageSucceeded,
		IdempotencyKey: "pipe-a:parse", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	currentVersion := "wv-1"
	if err := db.Create(&models.WikiPage{
		ID: "page-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", PageType: "document", Slug: "page",
		Title: "Page", CurrentVersionID: &currentVersion, PublishedVersionID: &currentVersion, Status: "PUBLISHED", ACLVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := db.Create(&models.WikiPageVersion{
		ID: "wv-1", WikiPageID: "page-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Version: 1,
		Content: "content", SourceHash: "hash", ReviewStatus: "APPROVED", ACLVersion: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed page version: %v", err)
	}
	if err := db.Create(&models.WikiPageSource{
		TenantID: "tenant-a", KnowledgeBaseID: "kb-a", WikiPageVersionID: "wv-1",
		DocumentID: "doc-a", DocumentVersionID: "ver-a", ChunkID: "chunk-a", SourcePath: "a.md",
	}).Error; err != nil {
		t.Fatalf("seed page source: %v", err)
	}
	if err := db.Create(&models.ProjectionRun{
		ID: "run-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", EvidencePackageID: "pkg-1",
		ProjectionType: knowledgeprojection.ProjectionTypeWiki, Status: knowledgeprojection.ProjectionReady,
		SourceHash: "hash", PromptVersion: "prompt", Model: "model", ACLVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed projection run: %v", err)
	}

	identities, err := identity.NewService(db, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600,
		APIKeyPepper: "pepper", DevAuthEnabled: true,
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	projection := knowledgeprojection.NewService(knowledgeprojection.Options{
		DB: db, Authz: httpProjectionAuthorizer{}, Embedder: ingestion.NewLocalEmbedder(8),
	})
	knowledgeBases := knowledgebase.NewService(db, authzService, knowledgebase.EmbeddingBinding{Model: "local", Dimension: 8})
	objects := &httpDocObjects{data: map[string][]byte{
		"src-a":  []byte("raw source"),
		"src-b":  []byte("second source"),
		"norm-a": []byte(`{"schema_version":"NormalizedDocumentV1","title":"Doc","language":"en","parser_version":"p1","blocks":[{"block_id":"b1","kind":"text","text":"hello","heading_path":"H","source_path":"a.md"}]}`),
	}}
	ingestionService := ingestion.NewService(db, authzService, objects, nil, 1<<20, 30, ingestion.Quota{})
	cipher, err := secrets.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	dataSources := datasource.NewService(db, authzService, cipher, ingestionService, datasource.FetchOptions{AllowPrivateNetworks: true}, 500)
	deployment := tenantconfig.Deployment{Chunk: tenantconfig.ChunkPolicy{Size: 800, Overlap: 80, MinSize: 100, Markers: []string{"\n## "}}}
	tenantConfig := tenantconfig.NewService(db, authzService, deployment)
	configPreview := configpreview.NewService(configpreview.Options{DB: db, Authz: authzService, Deployment: deployment, EmbeddingModel: "local", EmbeddingDimension: 8})
	configHealth := confighealth.NewService(confighealth.Options{
		DB: db, AppEnv: "test", EmbeddingDimension: 8, Now: time.Now,
		CheckIndexes:   func(context.Context) (confighealth.IndexReport, error) { return confighealth.IndexReport{}, nil },
		IndexDimension: func(context.Context) (int, bool, error) { return 8, true, nil },
		ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
			return modelregistry.ProviderTestResult{Healthy: true}, nil
		},
		ProbeProvider: func(context.Context, string, modelregistry.Actor, string) (modelregistry.ProviderTestResult, error) {
			return modelregistry.ProviderTestResult{Healthy: true}, nil
		},
	})
	conversationService := conversation.NewService(conversation.Options{
		Stores: conversation.NewPostgresStores(db), Authz: authzService, Timeout: 5 * time.Second,
	})
	modelRegistry, err := modelregistry.New(db, modelregistry.Options{
		Now:           time.Now,
		ProbeProvider: func(context.Context, modelregistry.ProviderRoute) error { return nil },
	})
	if err != nil {
		t.Fatalf("model registry: %v", err)
	}
	cfg := config.Config{
		AppEnv: "test", MaxFileBytes: 1 << 20,
		Retrieval: config.RetrievalConfig{
			TopK: 8, EmbeddingTopK: 20, KeywordTopK: 20, VectorThreshold: 0.2,
			RerankTopK: 8, RerankThreshold: -10, VectorEFSearch: 40, VectorOversample: 1,
			VectorIterativeScan: true, ContextTokenLimit: 3000, AdjacentExpansion: 1,
			MaxAdjacentChunks: 4, RRFK: 60, ExpansionScoreFactor: 0.5,
		},
		Auth: config.AuthConfig{DevAuthEnabled: true, CookieName: "rag_session"},
	}
	router := gin.New()
	RegisterRoutes(router, cfg, RouteDeps{
		Identity: identities, Authorization: authzService, Limiter: ratelimit.New(0),
		Projection: projection, KnowledgeBase: knowledgeBases, Ingestion: ingestionService,
		DataSources: dataSources, TenantConfig: tenantConfig, ConfigPreview: configPreview, ConfigHealth: configHealth,
		Conversation: conversationService, Models: modelRegistry,
		Tasks: ingestion.NewTaskInspector(db, nil),
		Ops:   ops.NewService(httpOpsStore{}), Metrics: observability.New(),
	}, slog.Default())
	return router, db
}

func projectionRequest(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Dev-User-ID", "owner-a")
	request.Header.Set("X-Dev-Tenant-ID", "tenant-a")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestProjectionRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)
	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/projection", "", http.StatusOK},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/wiki/pages", "", http.StatusOK},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/wiki/runs?limit=1", "", http.StatusOK},
		{http.MethodGet, "/api/v1/wiki/runs/run-1", "", http.StatusOK},
		{http.MethodGet, "/api/v1/wiki/pages/page-1", "", http.StatusOK},
		{http.MethodGet, "/api/v1/wiki/pages/page-1/versions", "", http.StatusOK},
		{http.MethodPost, "/api/v1/wiki/pages/page-1/versions/wv-1/review", `{"action":"approve"}`, http.StatusConflict},
		{http.MethodPost, "/api/v1/wiki/pages/page-1/versions/wv-1/publish", "", http.StatusOK},
		{http.MethodPost, "/api/v1/wiki/pages/page-1/rollback", `{"version_id":"wv-1"}`, http.StatusOK},
		{http.MethodGet, "/api/v1/wiki/runs/missing", "", http.StatusNotFound},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/knowledge/search?q=hello", "", http.StatusInternalServerError},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/graph/query?q=x", "", http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, testCase.body)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}

func TestOperationsRoutesAndModelHealth(t *testing.T) {
	router, db := newProjectionHTTPRouter(t)
	now := time.Now().UTC()
	if err := db.Create(&models.TaskInbox{IdempotencyKey: "run-1:parse", TaskType: "ingestion", Status: "SUCCEEDED", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	if err := db.Create(&models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run-1:parse", Payload: "{}",
	}).Error; err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/ops/tasks/run-1:parse", http.StatusOK},
		{http.MethodGet, "/api/v1/ops/dead-letters", http.StatusOK},
		{http.MethodPost, "/api/v1/ops/dead-letters/not-a-number/retry", http.StatusNotFound},
		{http.MethodPost, "/api/v1/ops/dead-letters/1/discard", http.StatusNotFound},
		{http.MethodPost, "/api/v1/ops/pipelines/missing/cancel", http.StatusNotFound},
		{http.MethodGet, "/api/v1/ops/metrics/prometheus", http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, "")
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}

	var builder strings.Builder
	statuses := []modelregistry.HealthStatus{
		{Model: "chat", Provider: "openai", Version: "v1", Healthy: true},
		{Model: `weird"model`, Provider: "line\nprovider", Healthy: false},
	}
	if err := writeModelHealth(&builder, statuses); err != nil {
		t.Fatalf("writeModelHealth: %v", err)
	}
	if !strings.Contains(builder.String(), `model_health{model="chat",provider="openai",version="v1"} 1`) {
		t.Fatalf("model health output = %q", builder.String())
	}
	if !strings.Contains(builder.String(), `model="weird\"model"`) {
		t.Fatalf("escaped model label missing: %q", builder.String())
	}
	if !strings.Contains(builder.String(), `provider="line\nprovider"`) {
		t.Fatalf("escaped provider label missing: %q", builder.String())
	}
	if err := writeModelHealth(&strings.Builder{}, nil); err != nil {
		t.Fatalf("empty model health = %v", err)
	}
	if got := prometheusLabel("a\\b\"c\nd"); got != `a\\b\"c\nd` {
		t.Fatalf("prometheusLabel = %q", got)
	}
}

func TestKnowledgeBaseRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)
	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/knowledge-bases", "", http.StatusOK},
		{http.MethodPost, "/api/v1/knowledge-bases", `{"name":"New KB"}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/knowledge-bases", `{}`, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a", "", http.StatusOK},
		{http.MethodGet, "/api/v1/knowledge-bases/missing", "", http.StatusForbidden},
		{http.MethodPatch, "/api/v1/knowledge-bases/kb-a", `{"name":"Renamed"}`, http.StatusOK},
		{http.MethodPatch, "/api/v1/knowledge-bases/kb-a", `{"retrieval_config":{"unknown_key":1}}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/v1/knowledge-bases/kb-a", `{bad json`, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/configuration", "", http.StatusOK},
		{http.MethodPut, "/api/v1/knowledge-bases/kb-a/retrieval-config", "{}", http.StatusOK},
		{http.MethodPut, "/api/v1/knowledge-bases/kb-a/retrieval-config", `{"unknown_key":1}`, http.StatusBadRequest},
		{http.MethodPut, "/api/v1/knowledge-bases/kb-a/retrieval-config", "{bad", http.StatusBadRequest},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/members", "", http.StatusOK},
		{http.MethodPost, "/api/v1/knowledge-bases/kb-a/members", `{"user_id":"ghost","role":"viewer"}`, http.StatusNotFound},
		{http.MethodDelete, "/api/v1/knowledge-bases/kb-a/members/ghost", "", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/knowledge-bases/kb-a", "", http.StatusNoContent},
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a", "", http.StatusForbidden},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, testCase.body)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}
