package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/gin-gonic/gin"
)

// The error contract is part of the API surface: a documented constraint
// violation must be a 400 with a stable code, and an unexpected failure must
// not leak its message.
func TestWriteErrorMapsDomainAndValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"range violation", fmt.Errorf("top_k must be between 0 and 200, got 900"), http.StatusBadRequest, "invalid_request"},
		{"threshold violation", fmt.Errorf("vector_threshold must be between 0 and 1, got 1.5"), http.StatusBadRequest, "invalid_request"},
		{"negative bound", fmt.Errorf("adjacent_expansion must not be negative"), http.StatusBadRequest, "invalid_request"},
		{"missing field", fmt.Errorf("query is required"), http.StatusBadRequest, "invalid_request"},
		{"not ready", apperror.ErrNotReady, http.StatusConflict, "not_ready"},
		{"forbidden", apperror.ErrForbidden, http.StatusForbidden, "forbidden"},
		{"not found", apperror.ErrNotFound, http.StatusNotFound, "not_found"},
		{"unauthenticated", apperror.ErrUnauthenticated, http.StatusUnauthorized, "unauthenticated"},
		{"conflict", apperror.ErrConflict, http.StatusConflict, "conflict"},
		{"rate limited", apperror.ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{"quota exceeded", apperror.ErrQuotaExceeded, http.StatusTooManyRequests, "quota_exceeded"},
		{"content unavailable", apperror.ErrContentUnavailable, http.StatusConflict, "content_unavailable"},
		// A storage path ending in .json must not be mistaken for a JSON
		// decoding error and echoed back with a 400.
		{"object path is not invalid json", errors.New("get object tenants/t1/normalized.json: The specified key does not exist."), http.StatusInternalServerError, "internal_error"},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			current := testCase.err
			router.GET("/probe", func(c *gin.Context) { writeError(c, current) })
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/probe", nil))

			if recorder.Code != testCase.status {
				t.Fatalf("expected status %d, got %d", testCase.status, recorder.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body["error"] != testCase.code {
				t.Fatalf("expected code %q, got %q", testCase.code, body["error"])
			}
			if testCase.status >= http.StatusInternalServerError {
				if _, leaked := body["message"]; leaked {
					t.Fatalf("internal errors must not leak their message: %v", body)
				}
			} else if body["message"] == "" {
				t.Fatalf("client errors must explain themselves: %v", body)
			}
		})
	}
}

// A wrapped content_unavailable error still carries the storage key, but the
// response must only contain the fixed explanation.
func TestContentUnavailableDoesNotEchoStorageKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	err := fmt.Errorf("read object tenants/t1/kb/documents/d/versions/v/normalized.json: %w", apperror.ErrContentUnavailable)
	router.GET("/probe", func(c *gin.Context) { writeError(c, err) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "tenants/") || strings.Contains(body, "normalized.json") {
		t.Fatalf("response leaked a storage key: %s", body)
	}
	if !strings.Contains(body, "content_unavailable") {
		t.Fatalf("response is missing the stable code: %s", body)
	}
}
