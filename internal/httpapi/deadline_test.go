package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/config"
)

func deadlineRouter(cfg config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	report := func(c *gin.Context) {
		_, ok := c.Request.Context().Deadline()
		c.JSON(http.StatusOK, gin.H{"deadline": ok})
	}
	r := gin.New()
	r.Use(requestDeadline(cfg))
	r.GET("/api/v1/slow", report)
	r.POST("/api/v1"+conversationSessionsPath+answerMessagePath, report)
	r.GET("/api/v1/requests/:requestID/events", report)
	r.POST("/api/v1"+documentUploadPath, report)
	r.POST("/api/v1/ops/reconcile", report)
	r.GET("/api/v1/audit", report)
	r.GET("/api/v1/hung", func(c *gin.Context) {
		<-c.Request.Context().Done()
		writeError(c, c.Request.Context().Err())
	})
	return r
}

func deadlineOf(t *testing.T, router *gin.Engine, method, path string) bool {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d, want 200", method, path, recorder.Code)
	}
	return strings.Contains(recorder.Body.String(), `"deadline":true`)
}

func TestRequestDeadlineAppliesByDefault(t *testing.T) {
	router := deadlineRouter(config.Config{RequestTimeoutSeconds: 60})
	if !deadlineOf(t, router, http.MethodGet, "/api/v1/slow") {
		t.Fatalf("default route must carry a deadline")
	}
}

func TestRequestDeadlineSkipsStreamingRoutes(t *testing.T) {
	router := deadlineRouter(config.Config{RequestTimeoutSeconds: 60})
	if deadlineOf(t, router, http.MethodPost, "/api/v1/sessions/s1/messages") {
		t.Fatalf("answer stream must be exempt from the request deadline")
	}
	if deadlineOf(t, router, http.MethodGet, "/api/v1/requests/r1/events") {
		t.Fatalf("event replay stream must be exempt from the request deadline")
	}
}

func TestRequestDeadlineOverridesLongRoutes(t *testing.T) {
	router := deadlineRouter(config.Config{RequestTimeoutSeconds: 60})
	for _, probe := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/knowledge-bases/kb1/documents"},
		{http.MethodPost, "/api/v1/ops/reconcile"},
		{http.MethodGet, "/api/v1/audit"},
	} {
		if !deadlineOf(t, router, probe.method, probe.path) {
			t.Fatalf("%s %s must keep a (longer) deadline", probe.method, probe.path)
		}
	}
}

func TestRequestDeadlineExpiryMapsToGatewayTimeout(t *testing.T) {
	router := deadlineRouter(config.Config{RequestTimeoutSeconds: 1})
	recorder := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/hung", nil))
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("deadline did not fire in time: %s", elapsed)
	}
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusGatewayTimeout)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"timeout"`) {
		t.Fatalf("body = %s, want timeout code", body)
	}
}

func TestRouteDeadlineBudgets(t *testing.T) {
	if budget, ok := routeDeadline(http.MethodPost, "/api/v1/sessions/:id/messages"); !ok || budget != 0 {
		t.Fatalf("answer stream budget = %s ok=%v, want exempt", budget, ok)
	}
	if budget, ok := routeDeadline(http.MethodPost, "/api/v1"+documentUploadPath); !ok || budget != uploadDeadline {
		t.Fatalf("upload budget = %s ok=%v, want %s", budget, ok, uploadDeadline)
	}
	if budget, ok := routeDeadline(http.MethodGet, "/api/v1/documents/:id/preview"); !ok || budget != transferDeadline {
		t.Fatalf("preview budget = %s ok=%v, want %s", budget, ok, transferDeadline)
	}
	if _, ok := routeDeadline(http.MethodGet, "/api/v1/unknown"); ok {
		t.Fatalf("unlisted route must fall back to the default budget")
	}
}
