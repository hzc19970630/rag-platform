package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/gin-gonic/gin"
)

func TestHealthEndpoint(t *testing.T) {
	router := NewRouter(config.Config{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
}

func TestReadinessReportsUnavailableDependencies(t *testing.T) {
	cfg := config.Config{
		AppEnv:        "test",
		DatabaseURL:   "postgres://user:pass@127.0.0.1:1/db",
		RedisAddr:     "127.0.0.1:1",
		DocreaderAddr: "127.0.0.1:1",
		S3Endpoint:    "127.0.0.1:1",
	}
	router := NewRouter(cfg)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recorder.Code)
	}
}

func TestRequestBodyLimitRejectsOversizedJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestBodyLimit(8))
	router.POST("/echo", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"query":"too large"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "payload_too_large") {
		t.Fatalf("expected payload_too_large, got %s", recorder.Body.String())
	}
}

func TestRequestBodyLimitSkipsOnlyUploadRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestBodyLimit(8))
	router.POST("/api/v1"+documentUploadPath, func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	body := "--boundary\r\nmore than eight bytes\r\n--boundary--\r\n"
	request := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/kb-1/documents", strings.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("the upload route must enforce its own cap, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// The multipart header is caller-controlled. Only the matched upload route may
// opt out of the JSON cap; a forged header on a JSON endpoint must not.
func TestRequestBodyLimitIgnoresSpoofedMultipartHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestBodyLimit(8))
	router.POST("/api/v1/search", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader("more than eight bytes"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for a spoofed multipart header, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequestLoggerIncludesCorrelationFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	router := gin.New()
	router.Use(requestLogger(logger))
	router.Use(func(c *gin.Context) {
		c.Set(accessContextKey, authz.AccessContext{Principal: authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}})
		c.Next()
	})
	router.GET("/api/v1/me", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	request.Header.Set("X-Request-ID", "req-1")
	router.ServeHTTP(httptest.NewRecorder(), request)

	line := output.String()
	for _, want := range []string{`"request_id":"req-1"`, `"tenant_id":"tenant-a"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("access log must contain %s, got %s", want, line)
		}
	}
}

func TestDevelopmentCORSPreflight(t *testing.T) {
	router := NewRouter(config.Config{AppEnv: "development"})
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/knowledge-bases", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Access-Control-Request-Headers", "idempotency-key")
	request.Header.Set("Access-Control-Request-Method", http.MethodPut)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPut) {
		t.Fatalf("expected PUT in CORS allow methods, got %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("expected development CORS origin, got %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "idempotency-key") {
		t.Fatalf("expected idempotency-key in CORS allow headers, got %q", got)
	}
}
