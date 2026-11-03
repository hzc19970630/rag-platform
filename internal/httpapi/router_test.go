package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/rag-platform/internal/config"
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
