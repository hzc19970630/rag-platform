package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerServesOnlyGetMetrics(t *testing.T) {
	handler := newMetricsHandler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics status: %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("GET /metrics content type: %q", contentType)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status: %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ops/metrics", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("non-metrics path status: %d", recorder.Code)
	}
}
