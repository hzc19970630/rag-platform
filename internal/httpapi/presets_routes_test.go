package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func presetRouterRequest(t *testing.T, router http.Handler, user, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Dev-User-ID", user)
	request.Header.Set("X-Dev-Tenant-ID", "tenant-a")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

type presetDetailBody struct {
	Source   string         `json:"source"`
	Manifest map[string]any `json:"manifest"`
}

func decodePresetDetail(t *testing.T, recorder *httptest.ResponseRecorder) presetDetailBody {
	t.Helper()
	var detail presetDetailBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode preset detail: %v, body = %s", err, recorder.Body.String())
	}
	return detail
}

func presetWriteBody(t *testing.T, manifest map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"manifest": manifest})
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	return string(raw)
}

func TestPresetRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)

	list := presetRouterRequest(t, router, "owner-a", http.MethodGet, "/api/v1/presets", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listed struct {
		Items []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	foundBuiltin := false
	for _, item := range listed.Items {
		if item.Name == "general-enterprise" && item.Source == "builtin" {
			foundBuiltin = true
		}
	}
	if !foundBuiltin {
		t.Fatalf("builtin preset missing from list: %+v", listed.Items)
	}

	builtinDetail := presetRouterRequest(t, router, "owner-a", http.MethodGet, "/api/v1/presets/general-enterprise/1.0.0", "")
	if builtinDetail.Code != http.StatusOK || decodePresetDetail(t, builtinDetail).Source != "builtin" {
		t.Fatalf("builtin detail = %d, body = %s", builtinDetail.Code, builtinDetail.Body.String())
	}
	if missing := presetRouterRequest(t, router, "owner-a", http.MethodGet, "/api/v1/presets/missing-pack/1.0.0", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing detail status = %d", missing.Code)
	}

	clone := presetRouterRequest(t, router, "owner-a", http.MethodPost,
		"/api/v1/presets/general-enterprise/1.0.0/clone", `{"name":"team-pack","version":"1.0.0"}`)
	if clone.Code != http.StatusCreated {
		t.Fatalf("clone status = %d, body = %s", clone.Code, clone.Body.String())
	}
	cloned := decodePresetDetail(t, clone)
	if cloned.Source != "tenant" {
		t.Fatalf("clone source = %q", cloned.Source)
	}
	cloned.Manifest["retrieval"].(map[string]any)["top_k"] = float64(5)
	updated := presetRouterRequest(t, router, "owner-a", http.MethodPut, "/api/v1/presets/team-pack/1.0.0", presetWriteBody(t, cloned.Manifest))
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	reread := presetRouterRequest(t, router, "owner-a", http.MethodGet, "/api/v1/presets/team-pack/1.0.0", "")
	if got := decodePresetDetail(t, reread).Manifest["retrieval"].(map[string]any)["top_k"]; got != float64(5) {
		t.Fatalf("updated top_k = %v", got)
	}

	builtinManifest := decodePresetDetail(t, builtinDetail).Manifest
	if builtinPut := presetRouterRequest(t, router, "owner-a", http.MethodPut,
		"/api/v1/presets/general-enterprise/1.0.0", presetWriteBody(t, builtinManifest)); builtinPut.Code != http.StatusBadRequest {
		t.Fatalf("builtin update status = %d, body = %s", builtinPut.Code, builtinPut.Body.String())
	}
	if builtinDelete := presetRouterRequest(t, router, "owner-a", http.MethodDelete, "/api/v1/presets/general-enterprise/1.0.0", ""); builtinDelete.Code != http.StatusBadRequest {
		t.Fatalf("builtin delete status = %d", builtinDelete.Code)
	}
	if duplicate := presetRouterRequest(t, router, "owner-a", http.MethodPost,
		"/api/v1/presets/general-enterprise/1.0.0/clone", `{"name":"team-pack","version":"1.0.0"}`); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate clone status = %d, body = %s", duplicate.Code, duplicate.Body.String())
	}
	if badJSON := presetRouterRequest(t, router, "owner-a", http.MethodPost, "/api/v1/presets", "{bad"); badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d", badJSON.Code)
	}
	reserved := presetRouterRequest(t, router, "owner-a", http.MethodPost, "/api/v1/presets", presetWriteBody(t, builtinManifest))
	if reserved.Code != http.StatusBadRequest {
		t.Fatalf("reserved name create status = %d, body = %s", reserved.Code, reserved.Body.String())
	}

	imported := builtinManifest
	imported["name"] = "imported-pack"
	create := presetRouterRequest(t, router, "owner-a", http.MethodPost, "/api/v1/presets", presetWriteBody(t, imported))
	if create.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", create.Code, create.Body.String())
	}
	if viewerWrite := presetRouterRequest(t, router, "viewer-a", http.MethodPost, "/api/v1/presets", presetWriteBody(t, imported)); viewerWrite.Code != http.StatusForbidden {
		t.Fatalf("viewer write status = %d, body = %s", viewerWrite.Code, viewerWrite.Body.String())
	}

	for _, name := range []string{"team-pack", "imported-pack"} {
		if deleted := presetRouterRequest(t, router, "owner-a", http.MethodDelete, "/api/v1/presets/"+name+"/1.0.0", ""); deleted.Code != http.StatusNoContent {
			t.Fatalf("delete %s status = %d, body = %s", name, deleted.Code, deleted.Body.String())
		}
	}
}
