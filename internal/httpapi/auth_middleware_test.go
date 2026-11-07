package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/platform/observability"
)

func TestNestedProtectedGroupInheritsAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{AppEnv: "test", Auth: config.AuthConfig{DevAuthEnabled: false}}
	router := gin.New()
	api := router.Group("/api/v1")
	protected := api.Group("")
	protected.Use(authenticationMiddleware(cfg, MiddlewareDeps{}))
	nested := protected.Group("/new-surface")
	nested.GET("/items", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodGet, "/api/v1/new-surface/items", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("new protected subgroup status = %d, want 401", recorder.Code)
	}
}

func TestRegisterRoutesMountsAuthenticationOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{AppEnv: "test", Auth: config.AuthConfig{DevAuthEnabled: false}}
	router := NewRouterWithReadiness(cfg, slog.Default(), observability.New(), nil)
	RegisterRoutes(router, cfg, RouteDeps{}, slog.Default())

	request := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("protected route status = %d, want 401", recorder.Code)
	}
}
