package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestConversationRoutes(t *testing.T) {
	router, db := newProjectionHTTPRouter(t)
	now := time.Now().UTC()

	create := projectionRequest(t, router, http.MethodPost, "/api/v1/sessions", `{"knowledge_base_id":"kb-a","title":"Chat"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create session = %d, body = %s", create.Code, create.Body.String())
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil || session.ID == "" {
		t.Fatalf("decode session = %+v, %v", session, err)
	}

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/sessions", "", http.StatusOK},
		{http.MethodGet, "/api/v1/sessions/" + session.ID + "/messages", "", http.StatusOK},
		{http.MethodPost, "/api/v1/sessions/" + session.ID + "/messages", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/sessions/missing/messages", `{"query":"hello"}`, http.StatusNotFound},
		{http.MethodGet, "/api/v1/requests/req-1/events", "", http.StatusOK},
		{http.MethodGet, "/api/v1/requests/missing/events", "", http.StatusNotFound},
	}
	if err := db.Create(&models.Message{
		ID: "msg-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ConversationID: session.ID,
		UserID: "owner-a", RequestID: "req-1", Role: "user", Content: "hello", Status: "COMPLETED",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := db.Create(&models.Message{
		ID: "msg-tail", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ConversationID: session.ID,
		UserID: "owner-a", RequestID: "req-tail", Role: "user", Content: "hello", Status: "COMPLETED",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed tail message: %v", err)
	}
	if err := db.Create(&models.StreamEvent{
		RequestID: "req-1", TenantID: "tenant-a", UserID: "owner-a", Sequence: 1,
		EventType: "status", Payload: `{"state":"received"}`, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed stream event: %v", err)
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, testCase.body)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}

	replay := projectionRequest(t, router, http.MethodGet, "/api/v1/requests/req-1/events", "")
	if !strings.Contains(replay.Body.String(), "status") || !strings.Contains(replay.Body.String(), "req-1") {
		t.Fatalf("replay body = %s", replay.Body.String())
	}

	tail := projectionRequest(t, router, http.MethodGet, "/api/v1/requests/req-tail/events?stream=1", "")
	if tail.Code != http.StatusOK || !strings.Contains(tail.Body.String(), "tail_timeout") {
		t.Fatalf("tail stream = %d, body = %s", tail.Code, tail.Body.String())
	}

	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/sessions", "{bad"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid create = %d", recorder.Code)
	}
}

func TestModelProviderRoutes(t *testing.T) {
	router, db := newProjectionHTTPRouter(t)
	now := time.Now().UTC()
	if err := db.Create(&models.ModelProvider{
		ID: "mp-1", TenantID: "tenant-a", Name: "provider", Kind: "openai",
		BaseURL: "https://example.com/v1", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/models/catalog", "", http.StatusOK},
		{http.MethodGet, "/api/v1/models/providers", "", http.StatusOK},
		{http.MethodPost, "/api/v1/models/providers", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/models/providers", `{"name":"p","kind":"openai_compatible","base_url":"https://example.com","models":[]}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/models/providers/missing/test", "", http.StatusNotFound},
		{http.MethodPatch, "/api/v1/models/providers/missing", `{"name":"p","kind":"openai_compatible","base_url":"https://example.com","models":[]}`, http.StatusNotFound},
		{http.MethodDelete, "/api/v1/models/providers/missing", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/models/providers/mp-1/test", "", http.StatusOK},
		{http.MethodDelete, "/api/v1/models/providers/mp-1", "", http.StatusNoContent},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, testCase.body)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}
