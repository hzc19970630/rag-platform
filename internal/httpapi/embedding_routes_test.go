package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/embeddingmigration"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type embHTTPAuthorizer struct{}

func (embHTTPAuthorizer) AuthorizeKnowledgeBase(context.Context, authz.Principal, string, string) (bool, error) {
	return true, nil
}

type embHTTPCatalog struct{}

func (embHTTPCatalog) ResolveEmbeddingModel(_ context.Context, _, requested string) (embedding.Binding, error) {
	return embedding.Binding{Model: requested, Dimension: 8}, nil
}

type embHTTPEmbedder struct{}

func (embHTTPEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = make([]float32, 8)
	}
	return vectors, nil
}
func (embHTTPEmbedder) Dimension() int { return 8 }
func (embHTTPEmbedder) Name() string   { return "target" }

type embHTTPFactory struct{}

func (embHTTPFactory) EmbedderForModel(context.Context, string, string) (embedding.Binding, embedding.Embedder, error) {
	return embedding.Binding{Model: "target", Dimension: 8}, embHTTPEmbedder{}, nil
}

type embHTTPWriter struct{}

func (embHTTPWriter) WriteVariants(context.Context, string, []embeddingmigration.VariantRecord) error {
	return nil
}

type embHTTPIndexer struct{}

func (embHTTPIndexer) EnsureIndex(context.Context, int) error { return nil }

type embHTTPPublisher struct{}

func (embHTTPPublisher) EnqueueEmbeddingMigration(context.Context, string, string, int) error {
	return nil
}

func newEmbeddingHTTPRouter(t *testing.T) http.Handler {
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
		&models.KnowledgeBase{}, &models.Membership{}, &models.AuthorizationRevision{},
		&models.AuditLog{}, &models.Document{}, &models.DocumentVersion{}, &models.Chunk{},
		&models.ChunkEmbedding{}, &models.EmbeddingMigration{},
	); err != nil {
		t.Fatalf("migrate embedding tables: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Create(&models.Membership{TenantID: "tenant-a", UserID: "owner-a", Role: "owner", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-e", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB", ACLVersion: 1,
		EmbeddingModel: "source", EmbeddingDimension: 4, RetrievalConfig: "{}", IndexingStrategy: "{}",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	if err := db.Create(&models.Chunk{
		ID: "chunk-e", TenantID: "tenant-a", KnowledgeBaseID: "kb-e", DocumentVersionID: "ver-e",
		ParentID: "parent-e", Ordinal: 0, Content: "chunk", ContentHash: "hash", Status: ingestion.StatusEvidenceReady,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
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
	embeddings, err := embeddingmigration.NewService(embeddingmigration.Options{
		DB: db, Authz: embHTTPAuthorizer{}, Models: embHTTPCatalog{}, EmbedderFactory: embHTTPFactory{},
		Writer: embHTTPWriter{}, Indexer: embHTTPIndexer{}, Publisher: embHTTPPublisher{}, BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("embedding migration service: %v", err)
	}
	cfg := config.Config{AppEnv: "test", Auth: config.AuthConfig{DevAuthEnabled: true, CookieName: "rag_session"}}
	router := gin.New()
	RegisterRoutes(router, cfg, RouteDeps{
		Identity: identities, Authorization: authzService, Limiter: ratelimit.New(0), Embeddings: embeddings,
	}, slog.Default())
	return router
}

func TestEmbeddingMigrationRoutes(t *testing.T) {
	router := newEmbeddingHTTPRouter(t)
	start := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-e/embedding-migration", `{"targetModel":"target"}`)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", start.Code, start.Body.String())
	}
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if job.ID == "" || job.Status != embeddingmigration.StatusPending {
		t.Fatalf("start job = %+v", job)
	}

	if recorder := projectionRequest(t, router, http.MethodGet, "/api/v1/knowledge-bases/kb-e/embedding-migration", ""); recorder.Code != http.StatusOK {
		t.Fatalf("latest status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-e/embedding-migration/"+job.ID+"/cutover", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("cutover pending status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-e/embedding-migration/"+job.ID+"/rollback", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("rollback pending status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-e/embedding-migration/"+job.ID+"/finalize", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("finalize pending status = %d", recorder.Code)
	}
	cancel := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-e/embedding-migration/"+job.ID+"/cancel", "")
	if cancel.Code != http.StatusOK || !strings.Contains(cancel.Body.String(), embeddingmigration.StatusCancelled) {
		t.Fatalf("cancel = %d, body = %s", cancel.Code, cancel.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-e/embedding-migration", `{bad`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid start status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodGet, "/api/v1/knowledge-bases/missing/embedding-migration", ""); recorder.Code != http.StatusForbidden && recorder.Code != http.StatusNotFound {
		t.Fatalf("missing knowledge base status = %d", recorder.Code)
	}
}
