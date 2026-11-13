package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	contract "github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"gopkg.in/yaml.v3"
)

const businessRouteCount = 101

type openAPIDocument struct {
	Paths      map[string]map[string]openAPIOperation `yaml:"paths"`
	Components struct {
		Schemas map[string]map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

type openAPIMedia struct {
	Schema map[string]any `yaml:"schema"`
}

type openAPIOperation struct {
	RequestBody *struct {
		Content map[string]openAPIMedia `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]struct {
		Content map[string]openAPIMedia `yaml:"content"`
	} `yaml:"responses"`
}

// TestOpenAPICoversEveryBusinessRoute keeps the generated document, the
// contracts/httpapi route manifest and the Gin router in lockstep. A route may
// not exist in only one of those places.
func TestOpenAPICoversEveryBusinessRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router, config.Config{AppEnv: "test"}, RouteDeps{}, slog.Default())

	spec := loadOpenAPI(t)
	registered := map[string]bool{}
	business := 0
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/v1/") {
			continue
		}
		business++
		path := openAPIPath(route.Path)
		registered[path+" "+strings.ToLower(route.Method)] = true
		if _, ok := spec.Paths[path][strings.ToLower(route.Method)]; !ok {
			t.Fatalf("route %s %s is missing from openapi.yaml", route.Method, route.Path)
		}
	}
	// The milestone baseline. Changing it means consciously adding or removing a
	// public route, so the manifest, the router and the document stay in step.
	if business != businessRouteCount {
		t.Fatalf("business routes = %d, want %d", business, businessRouteCount)
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			if !registered[path+" "+method] {
				t.Fatalf("openapi.yaml documents %s %s but no route registers it", strings.ToUpper(method), path)
			}
		}
	}

	if len(contract.Routes) != businessRouteCount {
		t.Fatalf("contract manifest routes = %d, want %d", len(contract.Routes), businessRouteCount)
	}
	documented := map[string]bool{}
	for _, route := range contract.Routes {
		key := route.Path + " " + strings.ToLower(route.Method)
		documented[key] = true
		if !registered[key] {
			t.Errorf("contract manifest documents %s %s but no route registers it", route.Method, route.Path)
		}
	}
	for key := range registered {
		if !documented[key] {
			t.Errorf("route %s is missing from the contracts/httpapi manifest", key)
		}
	}
}

// TestOpenAPIDeclaresRequestAndResponseSchemas keeps the generated TypeScript
// client meaningful: every JSON operation needs a schema on both sides.
func TestOpenAPIDeclaresRequestAndResponseSchemas(t *testing.T) {
	spec := loadOpenAPI(t)
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			if operation.RequestBody != nil {
				media, ok := operation.RequestBody.Content["application/json"]
				if !ok {
					media, ok = operation.RequestBody.Content["multipart/form-data"]
				}
				if !ok || len(media.Schema) == 0 {
					t.Errorf("%s %s: request body has no JSON or multipart schema", strings.ToUpper(method), path)
				}
			}
			for status, response := range operation.Responses {
				if !strings.HasPrefix(status, "2") || status == "204" {
					continue
				}
				for contentType, media := range response.Content {
					if contentType == "text/event-stream" {
						continue
					}
					if len(media.Schema) == 0 {
						t.Errorf("%s %s: response %s (%s) has no schema", strings.ToUpper(method), path, status, contentType)
					}
				}
			}
		}
	}
}

// TestOpenAPIRequestAndResponseSchemasResolve catches a request/response body
// that references a schema the document never defines, which would silently
// break both client generation and contract review.
func TestOpenAPIRequestAndResponseSchemasResolve(t *testing.T) {
	spec := loadOpenAPI(t)
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			var schemas []map[string]any
			if operation.RequestBody != nil {
				for _, media := range operation.RequestBody.Content {
					schemas = append(schemas, media.Schema)
				}
			}
			for _, response := range operation.Responses {
				for _, media := range response.Content {
					schemas = append(schemas, media.Schema)
				}
			}
			for _, schema := range schemas {
				ref, _ := schema["$ref"].(string)
				if ref == "" {
					continue
				}
				name := strings.TrimPrefix(ref, "#/components/schemas/")
				if name == ref {
					t.Errorf("%s %s: unsupported schema reference %q", strings.ToUpper(method), path, ref)
					continue
				}
				if _, ok := spec.Components.Schemas[name]; !ok {
					t.Errorf("%s %s: schema %q is not defined", strings.ToUpper(method), path, name)
				}
			}
		}
	}
}

// TestOpenAPIDocumentsMigratedRequestBodies pins every operation that decodes
// a JSON body to the schema generated for it. A handler cannot grow a new
// field unnoticed without updating the contract first.
func TestOpenAPIDocumentsMigratedRequestBodies(t *testing.T) {
	spec := loadOpenAPI(t)
	expected := map[string]string{
		"POST /api/v1/api-keys":                                        "APIKeyCreateRequest",
		"POST /api/v1/auth/dev-login":                                  "DevLoginRequest",
		"PATCH /api/v1/connectors/{id}":                                "ConnectorUpdateRequest",
		"POST /api/v1/graph/snapshots/{snapshotId}/review":             "GraphReviewRequest",
		"POST /api/v1/knowledge-bases":                                 "KnowledgeBaseCreateRequest",
		"PATCH /api/v1/knowledge-bases/{id}":                           "KnowledgeBaseUpdateRequest",
		"POST /api/v1/knowledge-bases/{id}/connectors":                 "ConnectorCreateRequest",
		"POST /api/v1/knowledge-bases/{id}/members":                    "KnowledgeBaseMemberCreateRequest",
		"PUT /api/v1/knowledge-bases/{id}/retrieval-config":            "RetrievalConfigUpdateRequest",
		"POST /api/v1/search":                                          "SearchRequest",
		"POST /api/v1/sessions":                                        "ConversationCreateRequest",
		"POST /api/v1/sessions/{id}/messages":                          "MessageCreateRequest",
		"POST /api/v1/tenant/members":                                  "TenantMemberCreateRequest",
		"PATCH /api/v1/tenant/members/{userID}":                        "TenantMemberUpdateRequest",
		"PATCH /api/v1/tenant/quota":                                   "TenantQuotaUpdateRequest",
		"PUT /api/v1/tenant/config":                                    "TenantConfigurationUpdateRequest",
		"POST /api/v1/tenant/config/test-connection":                   "ConfigConnectionTestRequest",
		"POST /api/v1/wiki/pages/{pageId}/rollback":                    "WikiRollbackRequest",
		"POST /api/v1/wiki/pages/{pageId}/versions/{versionId}/review": "WikiReviewRequest",
	}
	for operation, want := range expected {
		parts := strings.Split(operation, " ")
		method, path := parts[0], parts[1]
		op, ok := spec.Paths[path][strings.ToLower(method)]
		if !ok {
			t.Errorf("%s: operation is missing", operation)
			continue
		}
		if op.RequestBody == nil {
			t.Errorf("%s: request body is not documented", operation)
			continue
		}
		media, ok := op.RequestBody.Content["application/json"]
		if !ok {
			t.Errorf("%s: request body is not application/json", operation)
			continue
		}
		ref, _ := media.Schema["$ref"].(string)
		if !strings.HasSuffix(ref, "/"+want) {
			t.Errorf("%s: request schema = %q, want %q", operation, ref, want)
		}
	}

	// The upload route is multipart and is the only non-JSON write body.
	upload, ok := spec.Paths["/api/v1/knowledge-bases/{id}/documents"]["post"]
	if !ok || upload.RequestBody == nil {
		t.Fatal("upload operation must document its multipart body")
	}
	if _, ok := upload.RequestBody.Content["multipart/form-data"]; !ok {
		t.Fatal("upload operation must declare multipart/form-data")
	}
}

// TestOpenAPIRequestSchemasMatchContractDTOs compares each request schema with
// the contract type the manifest names, so the generated document cannot drift
// from the DTO the handler encodes.
func TestOpenAPIRequestSchemasMatchContractDTOs(t *testing.T) {
	cases := []struct {
		operation string
		schema    string
		dto       any
	}{
		{"POST /api/v1/api-keys", "APIKeyCreateRequest", contract.APIKeyCreateRequest{}},
		{"POST /api/v1/auth/dev-login", "DevLoginRequest", contract.DevLoginRequest{}},
		{"PATCH /api/v1/connectors/{id}", "ConnectorUpdateRequest", contract.ConnectorUpdateRequest{}},
		{"POST /api/v1/graph/snapshots/{snapshotId}/review", "GraphReviewRequest", contract.GraphReviewRequest{}},
		{"POST /api/v1/knowledge-bases", "KnowledgeBaseCreateRequest", contract.KnowledgeBaseCreateRequest{}},
		{"PATCH /api/v1/knowledge-bases/{id}", "KnowledgeBaseUpdateRequest", contract.KnowledgeBaseUpdateRequest{}},
		{"POST /api/v1/knowledge-bases/{id}/connectors", "ConnectorCreateRequest", contract.ConnectorCreateRequest{}},
		{"POST /api/v1/knowledge-bases/{id}/members", "KnowledgeBaseMemberCreateRequest", contract.KnowledgeBaseMemberCreateRequest{}},
		{"PUT /api/v1/knowledge-bases/{id}/retrieval-config", "RetrievalConfigOverride", contract.RetrievalConfigOverride{}},
		{"POST /api/v1/search", "SearchRequest", contract.SearchRequest{}},
		{"POST /api/v1/sessions", "ConversationCreateRequest", contract.ConversationCreateRequest{}},
		{"POST /api/v1/sessions/{id}/messages", "MessageCreateRequest", contract.MessageCreateRequest{}},
		{"POST /api/v1/tenant/members", "TenantMemberCreateRequest", contract.TenantMemberCreateRequest{}},
		{"PATCH /api/v1/tenant/members/{userID}", "TenantMemberUpdateRequest", contract.TenantMemberUpdateRequest{}},
		{"PATCH /api/v1/tenant/quota", "TenantQuotaUpdateRequest", contract.TenantQuotaUpdateRequest{}},
		{"POST /api/v1/wiki/pages/{pageId}/rollback", "WikiRollbackRequest", contract.WikiRollbackRequest{}},
		{"POST /api/v1/wiki/pages/{pageId}/versions/{versionId}/review", "WikiReviewRequest", contract.WikiReviewRequest{}},
	}
	for _, testCase := range cases {
		schema := loadOpenAPI(t).Components.Schemas[testCase.schema]
		if len(schema) == 0 {
			t.Errorf("%s: schema %q is not defined", testCase.operation, testCase.schema)
			continue
		}
		assertSchemaMatchesDTO(t, testCase.operation, schema, reflect.TypeOf(testCase.dto))
	}
}

// TestOpenAPIResponseSchemasMatchContractDTOs pins the success body of every
// modelled operation to the contract struct the handler emits. The comparison
// is top-level on purpose: nested types are checked by the generated reference
// resolution and by the views tests.
func TestOpenAPIResponseSchemasMatchContractDTOs(t *testing.T) {
	cases := []struct {
		operation string
		schema    string
		dto       any
	}{
		{"GET /api/v1/api-keys", "APIKeyList", contract.APIKeyList{}},
		{"POST /api/v1/api-keys", "APIKeyCreateResponse", contract.APIKeyCreateResponse{}},
		{"GET /api/v1/audit", "AuditEventList", contract.AuditEventList{}},
		{"GET /api/v1/auth/config", "AuthConfig", contract.AuthConfig{}},
		{"POST /api/v1/auth/dev-login", "DevLoginResponse", contract.DevLoginResponse{}},
		{"POST /api/v1/auth/refresh", "RefreshResponse", contract.RefreshResponse{}},
		{"GET /api/v1/chunks/{chunkID}", "Chunk", contract.Chunk{}},
		{"GET /api/v1/connectors/{id}", "Connector", contract.Connector{}},
		{"GET /api/v1/connectors/{id}/permissions", "ConnectorPermissions", contract.ConnectorPermissions{}},
		{"GET /api/v1/connectors/{id}/runs", "ConnectorRunList", contract.ConnectorRunList{}},
		{"GET /api/v1/connectors/{id}/runs/{runId}", "ConnectorRun", contract.ConnectorRun{}},
		{"POST /api/v1/connectors/{id}/cancel", "CancelResult", contract.CancelResult{}},
		{"GET /api/v1/documents/{id}", "DocumentView", contract.DocumentView{}},
		{"GET /api/v1/documents/{id}/chunks", "ChunkPage", contract.ChunkPage{}},
		{"GET /api/v1/documents/{id}/content", "DocumentPreview", contract.DocumentPreview{}},
		{"POST /api/v1/documents/{id}/restore", "DocumentEnvelope", contract.DocumentEnvelope{}},
		{"GET /api/v1/documents/{id}/status", "DocumentStatus", contract.DocumentStatus{}},
		{"GET /api/v1/documents/{id}/versions", "DocumentVersionPage", contract.DocumentVersionPage{}},
		{"POST /api/v1/documents/{id}/versions/{versionID}/activate", "DocumentView", contract.DocumentView{}},
		{"GET /api/v1/graph/runs/{runId}", "GraphRunDetail", contract.GraphRunDetail{}},
		{"GET /api/v1/graph/snapshots/{snapshotId}", "GraphSnapshotDetail", contract.GraphSnapshotDetail{}},
		{"GET /api/v1/knowledge-bases", "KnowledgeBaseList", contract.KnowledgeBaseList{}},
		{"GET /api/v1/knowledge-bases/{id}", "KnowledgeBase", contract.KnowledgeBase{}},
		{"GET /api/v1/knowledge-bases/{id}/configuration", "KnowledgeBaseConfiguration", contract.KnowledgeBaseConfiguration{}},
		{"GET /api/v1/knowledge-bases/{id}/connectors", "ConnectorList", contract.ConnectorList{}},
		{"POST /api/v1/knowledge-bases/{id}/connectors", "Connector", contract.Connector{}},
		{"GET /api/v1/knowledge-bases/{id}/documents", "DocumentPage", contract.DocumentPage{}},
		{"GET /api/v1/knowledge-bases/{id}/graph/query", "GraphQueryResult", contract.GraphQueryResult{}},
		{"POST /api/v1/knowledge-bases/{id}/graph/rebuild", "StartResult", contract.StartResult{}},
		{"GET /api/v1/knowledge-bases/{id}/graph/runs", "GraphRunList", contract.GraphRunList{}},
		{"GET /api/v1/knowledge-bases/{id}/graph/snapshots", "GraphSnapshotList", contract.GraphSnapshotList{}},
		{"GET /api/v1/knowledge-bases/{id}/knowledge/search", "KnowledgeSearchResult", contract.KnowledgeSearchResult{}},
		{"GET /api/v1/knowledge-bases/{id}/members", "KnowledgeBaseMemberList", contract.KnowledgeBaseMemberList{}},
		{"POST /api/v1/knowledge-bases/{id}/members", "KnowledgeBaseMember", contract.KnowledgeBaseMember{}},
		{"GET /api/v1/knowledge-bases/{id}/projection", "ProjectionState", contract.ProjectionState{}},
		{"GET /api/v1/knowledge-bases/{id}/wiki/pages", "WikiPageList", contract.WikiPageList{}},
		{"POST /api/v1/knowledge-bases/{id}/wiki/rebuild", "StartResult", contract.StartResult{}},
		{"GET /api/v1/knowledge-bases/{id}/wiki/runs", "WikiRunList", contract.WikiRunList{}},
		{"GET /api/v1/me", "MeResponse", contract.MeResponse{}},
		{"GET /api/v1/ops/dead-letters", "DeadLetterList", contract.DeadLetterList{}},
		{"POST /api/v1/ops/reconcile", "ReconcileReport", contract.ReconcileReport{}},
		{"GET /api/v1/ops/tasks/{key}", "TaskInspection", contract.TaskInspection{}},
		{"POST /api/v1/search", "EvidenceSet", contract.EvidenceSet{}},
		{"GET /api/v1/sessions", "ConversationList", contract.ConversationList{}},
		{"POST /api/v1/sessions", "Conversation", contract.Conversation{}},
		{"GET /api/v1/sessions/{id}/messages", "MessageList", contract.MessageList{}},
		{"GET /api/v1/tenant/members", "TenantMemberList", contract.TenantMemberList{}},
		{"POST /api/v1/tenant/members", "TenantMember", contract.TenantMember{}},
		{"GET /api/v1/tenant", "TenantOverview", contract.TenantOverview{}},
		{"GET /api/v1/tenant/quota", "TenantQuota", contract.TenantQuota{}},
		{"PATCH /api/v1/tenant/quota", "TenantQuota", contract.TenantQuota{}},
		{"GET /api/v1/wiki/pages/{pageId}", "WikiPageDetail", contract.WikiPageDetail{}},
		{"GET /api/v1/wiki/pages/{pageId}/versions", "WikiVersionList", contract.WikiVersionList{}},
		{"GET /api/v1/wiki/runs/{runId}", "WikiRunDetail", contract.WikiRunDetail{}},
	}
	spec := loadOpenAPI(t)
	for _, testCase := range cases {
		schema := spec.Components.Schemas[testCase.schema]
		if len(schema) == 0 {
			t.Errorf("%s: schema %q is not defined", testCase.operation, testCase.schema)
			continue
		}
		assertSchemaMatchesDTO(t, testCase.operation, schema, reflect.TypeOf(testCase.dto))
	}
}

// TestOpenAPIDeclaresWriteStatuses keeps the generated client from treating a
// 201/202/204 handler as a 200 response.
func TestOpenAPIDeclaresWriteStatuses(t *testing.T) {
	spec := loadOpenAPI(t)
	expected := map[string]string{
		"POST /api/v1/api-keys":                            "201",
		"POST /api/v1/connectors/{id}/runs/{runId}/cancel": "202",
		"POST /api/v1/connectors/{id}/runs/{runId}/retry":  "202",
		"POST /api/v1/connectors/{id}/sync":                "202",
		"POST /api/v1/documents/{id}/reparse":              "202",
		"POST /api/v1/knowledge-bases":                     "201",
		"POST /api/v1/knowledge-bases/{id}/connectors":     "201",
		"POST /api/v1/knowledge-bases/{id}/documents":      "202",
		"POST /api/v1/knowledge-bases/{id}/graph/rebuild":  "202",
		"POST /api/v1/knowledge-bases/{id}/members":        "201",
		"POST /api/v1/knowledge-bases/{id}/sync/cancel":    "202",
		"POST /api/v1/knowledge-bases/{id}/wiki/rebuild":   "202",
		"POST /api/v1/ops/dead-letters/{id}/discard":       "204",
		"POST /api/v1/ops/dead-letters/{id}/retry":         "204",
		"POST /api/v1/ops/pipelines/{id}/cancel":           "204",
		"POST /api/v1/sessions":                            "201",
		"POST /api/v1/tenant/members":                      "201",
		"PATCH /api/v1/tenant/members/{userID}":            "204",
	}
	for operation, want := range expected {
		parts := strings.Split(operation, " ")
		method, path := parts[0], parts[1]
		op := spec.Paths[path][strings.ToLower(method)]
		if _, ok := op.Responses[want]; !ok {
			t.Errorf("%s: success status %s is not documented", operation, want)
		}
	}
}

// TestAuthConfigResponseMatchesOpenAPI exercises the real handler and parses
// its body, proving the schema describes what the server emits.
func TestAuthConfigResponseMatchesOpenAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	identities, err := identity.NewService(nil, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	cfg := config.Config{AppEnv: "test"}
	cfg.Auth.DevAuthEnabled = true
	cfg.Auth.CookieName = "rag_session"
	router := gin.New()
	RegisterAuthRoutes(router, cfg, AuthDeps{Identity: identities}, MiddlewareDeps{}, slog.Default())

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("auth config status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode auth config: %v", err)
	}
	schema := loadOpenAPI(t).Components.Schemas["AuthConfig"]
	assertJSONBodyMatchesSchema(t, schema, body)
}

// TestUnauthenticatedResponseMatchesErrorSchema covers the shared error body
// through the real middleware, not a hand-built map.
func TestUnauthenticatedResponseMatchesErrorSchema(t *testing.T) {
	gin.SetMode(gin.TestMode)
	identities, err := identity.NewService(nil, identity.AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test",
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	router := gin.New()
	RegisterAuthRoutes(router, config.Config{AppEnv: "test"}, AuthDeps{Identity: identities}, MiddlewareDeps{}, slog.Default())

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	assertJSONBodyMatchesSchema(t, loadOpenAPI(t).Components.Schemas["Error"], body)
}

func assertSchemaMatchesDTO(t *testing.T, operation string, schema map[string]any, dto reflect.Type) {
	t.Helper()
	want := jsonPropertyNames(t, dto)
	got := schemaPropertyNames(schema)
	if missing, extra := setDiff(want, got), setDiff(got, want); len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: schema %s does not match DTO fields: missing %v, unexpected %v", operation, dto.Name(), missing, extra)
	}
	required := schemaRequiredNames(schema)
	for _, field := range bindingRequiredNames(t, dto) {
		if !required[field] {
			t.Errorf("%s: field %q is required by the handler but not by the schema", operation, field)
		}
	}
}

func assertJSONBodyMatchesSchema(t *testing.T, schema map[string]any, body map[string]any) {
	t.Helper()
	properties := schemaPropertyNames(schema)
	for field := range body {
		if !properties[field] {
			t.Errorf("response field %q is not declared in the schema", field)
		}
	}
	for field := range schemaRequiredNames(schema) {
		if _, ok := body[field]; !ok {
			t.Errorf("required response field %q is missing from the body", field)
		}
	}
}

func jsonPropertyNames(t *testing.T, dto reflect.Type) map[string]bool {
	t.Helper()
	for dto.Kind() == reflect.Pointer {
		dto = dto.Elem()
	}
	if dto.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct", dto)
	}
	names := map[string]bool{}
	for i := 0; i < dto.NumField(); i++ {
		field := dto.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		names[name] = true
	}
	return names
}

func bindingRequiredNames(t *testing.T, dto reflect.Type) []string {
	t.Helper()
	for dto.Kind() == reflect.Pointer {
		dto = dto.Elem()
	}
	var names []string
	for i := 0; i < dto.NumField(); i++ {
		field := dto.Field(i)
		if field.PkgPath != "" || !strings.Contains(field.Tag.Get("binding"), "required") {
			continue
		}
		names = append(names, strings.Split(field.Tag.Get("json"), ",")[0])
	}
	return names
}

func schemaPropertyNames(schema map[string]any) map[string]bool {
	names := map[string]bool{}
	properties, _ := schema["properties"].(map[string]any)
	for name := range properties {
		names[name] = true
	}
	return names
}

func schemaRequiredNames(schema map[string]any) map[string]bool {
	names := map[string]bool{}
	required, _ := schema["required"].([]any)
	for _, name := range required {
		if value, ok := name.(string); ok {
			names[value] = true
		}
	}
	return names
}

func setDiff(left, right map[string]bool) []string {
	var diff []string
	for name := range left {
		if !right[name] {
			diff = append(diff, name)
		}
	}
	sort.Strings(diff)
	return diff
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var spec openAPIDocument
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatalf("openapi.yaml has no paths")
	}
	return spec
}

func openAPIPath(ginPath string) string {
	parts := strings.Split(ginPath, "/")
	for index, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[index] = "{" + part[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}
