package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
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
		{"range violation", apperror.Invalidf("top_k must be between 0 and 200, got 900"), http.StatusBadRequest, "invalid_request"},
		{"threshold violation", apperror.Invalidf("vector_threshold must be between 0 and 1, got 1.5"), http.StatusBadRequest, "invalid_request"},
		{"negative bound", apperror.Invalidf("adjacent_expansion must not be negative"), http.StatusBadRequest, "invalid_request"},
		{"missing field", apperror.Invalidf("query is required"), http.StatusBadRequest, "invalid_request"},
		{"session override", invalidSessionOverride(t), http.StatusBadRequest, "invalid_request"},
		{"empty body", io.EOF, http.StatusBadRequest, "invalid_request"},
		{"truncated body", io.ErrUnexpectedEOF, http.StatusBadRequest, "invalid_request"},
		// A downstream failure may contain words that look like validation
		// prose. Only the error type may decide the response.
		{"internal message with required", errors.New("bucket policy is required"), http.StatusInternalServerError, "internal_error"},
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

// invalidSessionOverride runs the same resolution path a handler uses, so the
// contract test covers the error type handlers actually receive.
func invalidSessionOverride(t *testing.T) error {
	t.Helper()
	defaults := retrieval.Params{
		TopK: 10, EmbeddingTopK: 10, KeywordTopK: 10,
		VectorThreshold: 0.5, RerankTopK: 10, RerankThreshold: 0.5,
		ContextTokenLimit: 4000, RRFK: 60, ExpansionScoreFactor: 0.5,
	}
	_, err := retrieval.ResolveParams(defaults, "", `{"top_k":900}`)
	if err == nil {
		t.Fatal("expected an out-of-range session override to fail")
	}
	return err
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
