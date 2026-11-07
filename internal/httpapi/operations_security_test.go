package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The operator surface is tenant-scoped in the service layer. These tests pin
// the HTTP half: a tenant owner must not read another tenant's task or
// dead-letter rows, and a non-operator member is rejected before any query.
func TestOperationsRoutesDoNotCrossTenantBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.Membership{}, &models.AuthorizationRevision{}, &models.AuditLog{},
		&models.PipelineRun{}, &models.StageRun{}, &models.TaskInbox{},
		&models.OutboxEvent{}, &models.TaskDeadLetter{},
	); err != nil {
		t.Fatalf("migrate ops tables: %v", err)
	}

	now := time.Now()
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner"},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer"},
		{TenantID: "tenant-b", UserID: "owner-b", Role: "owner"},
	}).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := db.Create(&models.PipelineRun{
		ID: "pipe-b", PipelineType: "ingestion", TenantID: "tenant-b", KnowledgeBaseID: "kb-b",
		DocumentID: "doc-b", DocumentVersionID: "ver-b", RequestID: "req-b", TraceID: "trace-b",
		IdempotencyKey: "pipe-b", Status: "RUNNING", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: "pipe-b", Stage: "parse", Status: ingestion.StageProcessing,
		IdempotencyKey: "task-b", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: "task-b", Status: "RUNNING", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	if err := db.Create(&models.TaskDeadLetter{
		IdempotencyKey: "dead-b", TenantID: "tenant-b", KnowledgeBaseID: "kb-b", DocumentVersionID: "ver-b",
		TaskType: "ingestion", Stage: "embed", LastError: "provider down", Payload: "{}", Status: "PENDING",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed dead letter: %v", err)
	}

	identityService, err := identity.NewService(db, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	cfg := config.Config{AppEnv: "test"}
	cfg.Auth.DevAuthEnabled = true
	cfg.Auth.CookieName = "rag_session"
	router := gin.New()
	RegisterRoutes(router, cfg, RouteDeps{
		Identity:      identityService,
		Authorization: authzService,
		Limiter:       ratelimit.New(0),
		Tasks:         ingestion.NewTaskInspector(db, nil),
	}, slog.Default())

	request := func(userID, tenantID, path string) *httptest.ResponseRecorder {
		t.Helper()
		httpRequest := httptest.NewRequest(http.MethodGet, path, nil)
		httpRequest.Header.Set("X-Dev-User-ID", userID)
		httpRequest.Header.Set("X-Dev-Tenant-ID", tenantID)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httpRequest)
		return recorder
	}

	t.Run("another tenant's task is not found", func(t *testing.T) {
		recorder := request("owner-a", "tenant-a", "/api/v1/ops/tasks/task-b")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant task lookup = %d, want 404: %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("owning tenant can inspect the task", func(t *testing.T) {
		recorder := request("owner-b", "tenant-b", "/api/v1/ops/tasks/task-b")
		if recorder.Code != http.StatusOK {
			t.Fatalf("owner lookup = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Pipeline models.PipelineRun `json:"pipeline"`
			Inbox    *models.TaskInbox  `json:"inbox"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode inspection: %v", err)
		}
		if body.Pipeline.ID != "pipe-b" || body.Inbox == nil || body.Inbox.IdempotencyKey != "task-b" {
			t.Fatalf("unexpected inspection: %s", recorder.Body.String())
		}
	})

	t.Run("dead letters stay inside the tenant", func(t *testing.T) {
		recorder := request("owner-a", "tenant-a", "/api/v1/ops/dead-letters")
		if recorder.Code != http.StatusOK {
			t.Fatalf("tenant-a dead letters = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Items []models.TaskDeadLetter `json:"items"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode dead letters: %v", err)
		}
		if len(body.Items) != 0 {
			t.Fatalf("tenant-a must not see tenant-b dead letters: %+v", body.Items)
		}

		recorder = request("owner-b", "tenant-b", "/api/v1/ops/dead-letters")
		if recorder.Code != http.StatusOK {
			t.Fatalf("tenant-b dead letters = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode dead letters: %v", err)
		}
		if len(body.Items) != 1 || body.Items[0].IdempotencyKey != "dead-b" {
			t.Fatalf("tenant-b must see its own dead letter: %+v", body.Items)
		}
	})

	t.Run("non-operator membership is forbidden", func(t *testing.T) {
		recorder := request("viewer-a", "tenant-a", "/api/v1/ops/tasks/task-b")
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("viewer task lookup = %d, want 403: %s", recorder.Code, recorder.Body.String())
		}
	})
}
