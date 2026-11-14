package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectorRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)

	create := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/connectors",
		`{"name":"conn","type":"http_manifest","config":{"manifestUrl":"https://example.com/m.json"},"syncIntervalSeconds":3600}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}
	var connector struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &connector); err != nil {
		t.Fatalf("decode connector: %v", err)
	}
	if connector.ID == "" {
		t.Fatal("connector id is empty")
	}

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/connectors", "", http.StatusOK},
		{http.MethodGet, "/api/v1/connectors/" + connector.ID, "", http.StatusOK},
		{http.MethodPatch, "/api/v1/connectors/" + connector.ID, `{"name":"renamed"}`, http.StatusOK},
		{http.MethodGet, "/api/v1/connectors/" + connector.ID + "/permissions", "", http.StatusOK},
		{http.MethodGet, "/api/v1/connectors/" + connector.ID + "/runs", "", http.StatusOK},
		{http.MethodGet, "/api/v1/connectors/missing", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/knowledge-bases/kb-a/connectors", "{bad", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/knowledge-bases/kb-a/connectors", `{"name":"bad","type":"ftp","config":{"manifestUrl":"https://example.com/m.json"}}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/v1/connectors/" + connector.ID, "{bad", http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, testCase.body)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}

	sync := projectionRequest(t, router, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/sync", "")
	if sync.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d, body = %s", sync.Code, sync.Body.String())
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sync.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.ID == "" {
		t.Fatal("run id is empty")
	}
	if recorder := projectionRequest(t, router, http.MethodGet, "/api/v1/connectors/"+connector.ID+"/runs/"+run.ID, ""); recorder.Code != http.StatusOK {
		t.Fatalf("get run status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/runs/"+run.ID+"/retry", ""); recorder.Code != http.StatusConflict {
		t.Fatalf("retry pending status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/runs/"+run.ID+"/cancel", ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel run status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/runs/"+run.ID+"/retry", ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("retry cancelled status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/cancel", ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel connector status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/sync/cancel", ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("cancel knowledge base sync status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodDelete, "/api/v1/connectors/"+connector.ID, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestConfigurationRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/v1/tenant/config", "", http.StatusOK},
		{http.MethodPut, "/api/v1/tenant/config", `{"retrieval":{},"chunking":{}}`, http.StatusOK},
		{http.MethodGet, "/api/v1/tenant/config/health", "", http.StatusOK},
		{http.MethodPost, "/api/v1/tenant/config/test-connection", `{"capability":"chat"}`, http.StatusOK},
		{http.MethodPost, "/api/v1/config/preview", `{"scope":"tenant","retrieval":{},"chunking":{}}`, http.StatusOK},
		{http.MethodPut, "/api/v1/tenant/config", "{bad", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/config/preview", "{bad", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/tenant/config/test-connection", "{bad", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/tenant/config/test-connection", `{}`, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, testCase.body)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}

	viewer := httptest.NewRequest(http.MethodGet, "/api/v1/tenant/config", strings.NewReader(""))
	viewer.Header.Set("X-Dev-User-ID", "viewer-a")
	viewer.Header.Set("X-Dev-Tenant-ID", "tenant-a")
	viewerRecorder := httptest.NewRecorder()
	router.ServeHTTP(viewerRecorder, viewer)
	if viewerRecorder.Code != http.StatusForbidden {
		t.Fatalf("viewer tenant config = %d, want forbidden", viewerRecorder.Code)
	}
}
