package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	contract "github.com/hzc19970630/rag-platform/contracts/httpapi"
)

func TestKnowledgeSpaceRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)

	if recorder := projectionRequest(t, router, http.MethodGet, "/api/v1/knowledge-bases/kb-a/folders", ""); recorder.Code != http.StatusOK {
		t.Fatalf("list folders status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/folders", `{"name":"Contracts"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create folder status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var root contract.Folder
	if err := json.Unmarshal(recorder.Body.Bytes(), &root); err != nil || root.Depth != 1 || root.ParentID != "" {
		t.Fatalf("root folder = %+v, %v", root, err)
	}

	recorder = projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/folders", `{"name":"2026","parentId":"`+root.ID+`"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create child folder status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var child contract.Folder
	if err := json.Unmarshal(recorder.Body.Bytes(), &child); err != nil || child.Depth != 2 {
		t.Fatalf("child folder = %+v, %v", child, err)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/folders", `{"name":"contracts"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate folder name status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPatch, "/api/v1/folders/missing", `{"name":"x"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("forged folder id status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/tags", `{"name":"Finance"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create tag status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var tag contract.Tag
	if err := json.Unmarshal(recorder.Body.Bytes(), &tag); err != nil || tag.ID == "" {
		t.Fatalf("tag = %+v, %v", tag, err)
	}
	if recorder := projectionRequest(t, router, http.MethodGet, "/api/v1/knowledge-bases/kb-a/tags", ""); recorder.Code != http.StatusOK {
		t.Fatalf("list tags status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodPatch, "/api/v1/tags/"+tag.ID, `{"name":"Invoice"}`); recorder.Code != http.StatusOK {
		t.Fatalf("rename tag status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/documents/batch",
		`{"action":"move","documentIds":["doc-a","doc-missing"],"folderId":"`+child.ID+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("batch move status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var moved contract.BatchDocumentsResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &moved); err != nil || len(moved.Items) != 2 || !moved.Items[0].OK || moved.Items[1].ErrorCode != "not_found" {
		t.Fatalf("batch results = %+v, %v", moved, err)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/documents/batch",
		`{"action":"unknown","documentIds":["doc-a"]}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid batch action status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodDelete, "/api/v1/folders/"+child.ID, ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("non-empty folder delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/documents/batch",
		`{"action":"delete","documentIds":["doc-a"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("batch delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodDelete, "/api/v1/folders/"+child.ID, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("empty folder delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodDelete, "/api/v1/tags/"+tag.ID, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete tag status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
