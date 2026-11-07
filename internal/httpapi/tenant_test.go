package httpapi

import (
	"context"
	"encoding/json"
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
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/ratelimit"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"github.com/hzc19970630/rag-platform/internal/tenant"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestTenantRoutesExposeOverviewAndQuota(t *testing.T) {
	router, identities, db := newTenantHTTPTest(t)

	overview := tenantRequest(t, router, "owner-a", "tenant-a", http.MethodGet, "/api/v1/tenant", "")
	if overview.Code != http.StatusOK {
		t.Fatalf("overview status = %d: %s", overview.Code, overview.Body.String())
	}
	var overviewBody struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Members struct {
			Total   int64 `json:"total"`
			Owners  int64 `json:"owners"`
			Members int64 `json:"members"`
		} `json:"members"`
		Documents int64 `json:"documents"`
	}
	if err := json.Unmarshal(overview.Body.Bytes(), &overviewBody); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if overviewBody.ID != "tenant-a" || overviewBody.Name != "Tenant A" || overviewBody.Members.Total != 3 || overviewBody.Documents != 1 {
		t.Fatalf("unexpected overview: %s", overview.Body.String())
	}

	quota := tenantRequest(t, router, "owner-a", "tenant-a", http.MethodGet, "/api/v1/tenant/quota", "")
	if quota.Code != http.StatusOK {
		t.Fatalf("quota status = %d: %s", quota.Code, quota.Body.String())
	}
	var quotaBody struct {
		MaxDocuments struct {
			Value  int64  `json:"value"`
			Source string `json:"source"`
		} `json:"maxDocuments"`
		ChatRequestsPerDay struct {
			Value     int64  `json:"value"`
			Unlimited bool   `json:"unlimited"`
			Source    string `json:"source"`
		} `json:"chatRequestsPerDay"`
	}
	if err := json.Unmarshal(quota.Body.Bytes(), &quotaBody); err != nil {
		t.Fatalf("decode quota: %v", err)
	}
	if quotaBody.MaxDocuments.Value != 10 || quotaBody.MaxDocuments.Source != "deployment" {
		t.Fatalf("document quota = %+v, want deployment default", quotaBody.MaxDocuments)
	}
	if quotaBody.ChatRequestsPerDay.Value != 0 || !quotaBody.ChatRequestsPerDay.Unlimited || quotaBody.ChatRequestsPerDay.Source != "tenant" {
		t.Fatalf("chat quota = %+v, want explicit unlimited override", quotaBody.ChatRequestsPerDay)
	}

	updated := tenantRequest(t, router, "admin-a", "tenant-a", http.MethodPatch, "/api/v1/tenant/quota", `{"maxDocuments":2,"maxStorageBytes":4096}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("quota update status = %d: %s", updated.Code, updated.Body.String())
	}
	if !strings.Contains(updated.Body.String(), `"source":"tenant"`) || !strings.Contains(updated.Body.String(), `"value":2`) {
		t.Fatalf("quota update body = %s", updated.Body.String())
	}

	var stored models.Tenant
	if err := db.First(&stored, "id = ?", "tenant-a").Error; err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if !strings.Contains(stored.Quota, "max_documents") || strings.Contains(stored.Quota, "chat_requests_per_day") {
		t.Fatalf("PATCH must replace the override object, stored %s", stored.Quota)
	}

	// The created API key is an operator credential but must not be able to
	// change quotas: those actions stay attributable to a person.
	_, token, err := identities.CreateAPIKey(context.Background(), authz.Principal{SubjectID: "owner-a", TenantID: "tenant-a"}, identity.CreateAPIKeyInput{Name: "automation"})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	apiKeyUpdate := tenantRequestWithToken(t, router, token, http.MethodPatch, "/api/v1/tenant/quota", `{"maxDocuments":1}`)
	if apiKeyUpdate.Code != http.StatusForbidden {
		t.Fatalf("api-key quota update = %d, want 403: %s", apiKeyUpdate.Code, apiKeyUpdate.Body.String())
	}
}

func TestTenantRoutesRejectNonOperators(t *testing.T) {
	router, _, _ := newTenantHTTPTest(t)
	for _, path := range []string{"/api/v1/tenant", "/api/v1/tenant/quota"} {
		recorder := tenantRequest(t, router, "viewer-a", "tenant-a", http.MethodGet, path, "")
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s as viewer = %d, want 403: %s", path, recorder.Code, recorder.Body.String())
		}
	}
	recorder := tenantRequest(t, router, "viewer-a", "tenant-a", http.MethodPatch, "/api/v1/tenant/quota", `{}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("quota update as viewer = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}

	// A tenant owner cannot read or write another tenant's administration data.
	crossTenant := tenantRequest(t, router, "owner-b", "tenant-b", http.MethodGet, "/api/v1/tenant", "")
	if crossTenant.Code != http.StatusOK {
		t.Fatalf("tenant-b overview status = %d: %s", crossTenant.Code, crossTenant.Body.String())
	}
	if strings.Contains(crossTenant.Body.String(), "tenant-a") {
		t.Fatalf("tenant-b overview leaked tenant-a: %s", crossTenant.Body.String())
	}
}

func tenantRequest(t *testing.T, router *gin.Engine, userID, tenantID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Dev-User-ID", userID)
	request.Header.Set("X-Dev-Tenant-ID", tenantID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func tenantRequestWithToken(t *testing.T, router *gin.Engine, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func newTenantHTTPTest(t *testing.T) (*gin.Engine, *identity.Service, *gorm.DB) {
	t.Helper()
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
		&models.Tenant{}, &models.User{}, &models.Membership{}, &models.AuthorizationRevision{},
		&models.APIKey{}, &models.KnowledgeBase{}, &models.Document{}, &models.DocumentVersion{},
		&models.Message{}, &models.AuditLog{},
	); err != nil {
		t.Fatalf("migrate tenant tables: %v", err)
	}
	now := time.Now()
	if err := db.Create(&[]models.Tenant{
		{ID: "tenant-a", Name: "Tenant A", Quota: `{"chat_requests_per_day":0}`},
		{ID: "tenant-b", Name: "Tenant B", Quota: "{}"},
	}).Error; err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	if err := db.Create(&[]models.User{
		{ID: "owner-a", Email: "owner-a@example.com", DisplayName: "Owner A"},
		{ID: "admin-a", Email: "admin-a@example.com", DisplayName: "Admin A"},
		{ID: "viewer-a", Email: "viewer-a@example.com", DisplayName: "Viewer A"},
		{ID: "owner-b", Email: "owner-b@example.com", DisplayName: "Owner B"},
	}).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner"},
		{TenantID: "tenant-a", UserID: "admin-a", Role: "admin"},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer"},
		{TenantID: "tenant-b", UserID: "owner-b", Role: "owner"},
	}).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := db.Create(&[]models.KnowledgeBase{
		{ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB A"},
		{ID: "kb-b", TenantID: "tenant-b", OwnerID: "owner-b", Name: "KB B"},
	}).Error; err != nil {
		t.Fatalf("seed knowledge bases: %v", err)
	}
	if err := db.Create(&models.Document{
		ID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "owner-a",
		Filename: "a.pdf", MimeType: "application/pdf", Status: "READY", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: "ver-a", DocumentID: "doc-a", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		Version: 1, SizeBytes: 100, Status: "READY", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document version: %v", err)
	}

	identities, err := identity.NewService(db, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600, APIKeyPepper: "test-pepper",
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	quotaResolver := tenantquota.NewResolver(db, tenantquota.Limits{MaxDocuments: 10, MaxStorageBytes: 1000, ChatRequestsPerDay: 5})
	tenantService := tenant.NewService(db, authzService, quotaResolver)

	cfg := config.Config{AppEnv: "test"}
	cfg.Auth.DevAuthEnabled = true
	cfg.Auth.CookieName = "rag_session"
	router := gin.New()
	RegisterRoutes(router, cfg, RouteDeps{
		Identity: identities, Authorization: authzService,
		Limiter: ratelimit.New(0),
		Tenant:  tenantService,
	}, slog.Default())
	return router, identities, db
}
