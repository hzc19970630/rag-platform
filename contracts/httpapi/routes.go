package httpapi

import "net/http"

// Route is one HTTP operation this deployment serves. cmd/openapi reflects over
// the schema types to generate api/openapi/openapi.yaml, and the router
// contract test compares the manifest with the Gin route table, so a route
// cannot exist in only one of the two places.
type Route struct {
	Method      string
	Path        string
	OperationID string
	Tag         string
	Public      bool
	Request     *RequestBody
	Success     Response
	Errors      []int
}

// RequestBody names the wire DTO a handler binds. Schema is a typed zero value
// (for example (*APIKeyCreateRequest)(nil)) so reflection can recover the type
// without constructing a value.
type RequestBody struct {
	ContentType string
	Schema      any
	Required    bool
}

// Response describes the success body. Schema is nil when the status carries no
// body, and Binary marks a raw octet stream whose schema is not a DTO.
type Response struct {
	Status      int
	ContentType string
	Schema      any
	Binary      bool
	Description string
}

var (
	// Read and write failures share the standard envelope. Deletes that the
	// authorization layer rejects before parsing anything only document 401/403.
	commonErrors = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError}
	deleteErrors = []int{http.StatusUnauthorized, http.StatusForbidden}
)

func jsonBody(schema any) *RequestBody {
	return &RequestBody{ContentType: "application/json", Schema: schema, Required: true}
}

func ok(schema any) Response {
	return Response{Status: http.StatusOK, ContentType: "application/json", Schema: schema}
}

func created(schema any) Response {
	return Response{Status: http.StatusCreated, ContentType: "application/json", Schema: schema}
}

func accepted(schema any) Response {
	return Response{Status: http.StatusAccepted, ContentType: "application/json", Schema: schema}
}

func noContent() Response {
	return Response{Status: http.StatusNoContent}
}

func redirect() Response {
	return Response{Status: http.StatusFound, Description: "Redirect to the web application"}
}

func eventStream(schema any) Response {
	return Response{Status: http.StatusOK, ContentType: "text/event-stream", Schema: schema}
}

func plainText(schema any) Response {
	return Response{Status: http.StatusOK, ContentType: "text/plain", Schema: schema}
}

func binaryBody() Response {
	return Response{
		Status:      http.StatusOK,
		ContentType: "application/octet-stream",
		Binary:      true,
		Description: "Original file bytes with the stored MIME type",
	}
}

// Routes is the complete business surface under /api/v1. The health endpoints
// stay outside the contract: they are deployment probes, not API resources.
var Routes = []Route{
	// auth
	{Method: http.MethodGet, Path: "/api/v1/auth/config", OperationID: "get_auth_config", Tag: "auth", Public: true,
		Success: ok((*AuthConfig)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/auth/oidc/login", OperationID: "get_auth_oidc_login", Tag: "auth", Public: true,
		Success: redirect(), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/auth/callback", OperationID: "get_auth_callback", Tag: "auth", Public: true,
		Success: redirect(), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/auth/ldap/login", OperationID: "post_auth_ldap_login", Tag: "auth", Public: true,
		Request: jsonBody((*LDAPLoginRequest)(nil)), Success: ok((*DevLoginResponse)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/auth/dev-login", OperationID: "post_auth_dev_login", Tag: "auth", Public: true,
		Request: jsonBody((*DevLoginRequest)(nil)), Success: ok((*DevLoginResponse)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/auth/refresh", OperationID: "post_auth_refresh", Tag: "auth", Public: true,
		Success: ok((*RefreshResponse)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/auth/logout", OperationID: "post_auth_logout", Tag: "auth", Public: true,
		Success: noContent(), Errors: commonErrors},

	// identity
	{Method: http.MethodGet, Path: "/api/v1/me", OperationID: "get_me", Tag: "identity",
		Success: ok((*MeResponse)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/audit", OperationID: "get_audit", Tag: "identity",
		Success: ok((*AuditEventList)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/audit/authorization", OperationID: "get_audit_authorization", Tag: "identity",
		Success: ok((*AuditEventList)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/api-keys", OperationID: "get_api_keys", Tag: "identity",
		Success: ok((*APIKeyList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/api-keys", OperationID: "post_api_keys", Tag: "identity",
		Request: jsonBody((*APIKeyCreateRequest)(nil)), Success: created((*APIKeyCreateResponse)(nil)), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/api-keys/{id}", OperationID: "delete_api_keys_id", Tag: "identity",
		Success: noContent(), Errors: deleteErrors},
	{Method: http.MethodGet, Path: "/api/v1/tenant/members", OperationID: "get_tenant_members", Tag: "identity",
		Success: ok((*TenantMemberList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/tenant/members", OperationID: "post_tenant_members", Tag: "identity",
		Request: jsonBody((*TenantMemberCreateRequest)(nil)), Success: created((*TenantMember)(nil)), Errors: commonErrors},
	{Method: http.MethodPatch, Path: "/api/v1/tenant/members/{userID}", OperationID: "patch_tenant_members_userID", Tag: "identity",
		Request: jsonBody((*TenantMemberUpdateRequest)(nil)), Success: noContent(), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/tenant/members/{userID}", OperationID: "delete_tenant_members_userID", Tag: "identity",
		Success: noContent(), Errors: deleteErrors},
	{Method: http.MethodGet, Path: "/api/v1/tenant", OperationID: "get_tenant", Tag: "tenant",
		Success: ok((*TenantOverview)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/tenant/quota", OperationID: "get_tenant_quota", Tag: "tenant",
		Success: ok((*TenantQuota)(nil)), Errors: commonErrors},
	{Method: http.MethodPatch, Path: "/api/v1/tenant/quota", OperationID: "patch_tenant_quota", Tag: "tenant",
		Request: jsonBody((*TenantQuotaUpdateRequest)(nil)), Success: ok((*TenantQuota)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/tenant/config", OperationID: "get_tenant_config", Tag: "configuration",
		Success: ok((*TenantConfiguration)(nil)), Errors: commonErrors},
	{Method: http.MethodPut, Path: "/api/v1/tenant/config", OperationID: "put_tenant_config", Tag: "configuration",
		Request: jsonBody((*TenantConfigurationUpdateRequest)(nil)), Success: ok((*TenantConfiguration)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/tenant/config/health", OperationID: "get_tenant_config_health", Tag: "configuration",
		Success: ok((*ConfigHealth)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/tenant/config/test-connection", OperationID: "post_tenant_config_test_connection", Tag: "configuration",
		Request: jsonBody((*ConfigConnectionTestRequest)(nil)), Success: ok((*ConfigConnectionTestResult)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/config/preview", OperationID: "post_config_preview", Tag: "configuration",
		Request: jsonBody((*ConfigChangePreviewRequest)(nil)), Success: ok((*ConfigChangePreview)(nil)), Errors: commonErrors},

	// knowledge bases
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases", OperationID: "get_knowledge_bases", Tag: "knowledge-bases",
		Success: ok((*KnowledgeBaseList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases", OperationID: "post_knowledge_bases", Tag: "knowledge-bases",
		Request: jsonBody((*KnowledgeBaseCreateRequest)(nil)), Success: created((*KnowledgeBase)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}", OperationID: "get_knowledge_bases_id", Tag: "knowledge-bases",
		Success: ok((*KnowledgeBase)(nil)), Errors: commonErrors},
	{Method: http.MethodPatch, Path: "/api/v1/knowledge-bases/{id}", OperationID: "patch_knowledge_bases_id", Tag: "knowledge-bases",
		Request: jsonBody((*KnowledgeBaseUpdateRequest)(nil)), Success: ok((*KnowledgeBase)(nil)), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/knowledge-bases/{id}", OperationID: "delete_knowledge_bases_id", Tag: "knowledge-bases",
		Success: noContent(), Errors: deleteErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/configuration", OperationID: "get_knowledge_bases_id_configuration", Tag: "knowledge-bases",
		Success: ok((*KnowledgeBaseConfiguration)(nil)), Errors: commonErrors},
	{Method: http.MethodPut, Path: "/api/v1/knowledge-bases/{id}/retrieval-config", OperationID: "put_knowledge_bases_id_retrieval_config", Tag: "knowledge-bases",
		Request: jsonBody((*RetrievalConfigUpdateRequest)(nil)), Success: ok((*KnowledgeBaseConfiguration)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/members", OperationID: "get_knowledge_bases_id_members", Tag: "knowledge-bases",
		Success: ok((*KnowledgeBaseMemberList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/members", OperationID: "post_knowledge_bases_id_members", Tag: "knowledge-bases",
		Request: jsonBody((*KnowledgeBaseMemberCreateRequest)(nil)), Success: created((*KnowledgeBaseMember)(nil)), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/knowledge-bases/{id}/members/{userID}", OperationID: "delete_knowledge_bases_id_members_userID", Tag: "knowledge-bases",
		Success: noContent(), Errors: deleteErrors},

	// documents
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/documents", OperationID: "get_knowledge_bases_id_documents", Tag: "documents",
		Success: ok((*DocumentPage)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/documents", OperationID: "post_knowledge_bases_id_documents", Tag: "documents",
		Request: &RequestBody{ContentType: "multipart/form-data", Schema: (*DocumentUploadRequest)(nil), Required: true},
		Success: accepted((*DocumentView)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/documents/{id}", OperationID: "get_documents_id", Tag: "documents",
		Success: ok((*DocumentView)(nil)), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/documents/{id}", OperationID: "delete_documents_id", Tag: "documents",
		Success: noContent(), Errors: deleteErrors},
	{Method: http.MethodGet, Path: "/api/v1/documents/{id}/preview", OperationID: "get_documents_id_preview", Tag: "documents",
		Success: binaryBody(), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/documents/{id}/content", OperationID: "get_documents_id_content", Tag: "documents",
		Success: ok((*DocumentPreview)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/documents/{id}/chunks", OperationID: "get_documents_id_chunks", Tag: "documents",
		Success: ok((*ChunkPage)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/documents/{id}/status", OperationID: "get_documents_id_status", Tag: "documents",
		Success: ok((*DocumentStatus)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/documents/{id}/versions", OperationID: "get_documents_id_versions", Tag: "documents",
		Success: ok((*DocumentVersionPage)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/chunks/{chunkID}", OperationID: "get_chunks_chunkID", Tag: "documents",
		Success: ok((*Chunk)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/documents/{id}/reparse", OperationID: "post_documents_id_reparse", Tag: "documents",
		Success: accepted((*DocumentView)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/documents/{id}/versions/{versionID}/activate", OperationID: "post_documents_id_versions_versionID_activate", Tag: "documents",
		Success: ok((*DocumentView)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/documents/{id}/restore", OperationID: "post_documents_id_restore", Tag: "documents",
		Success: ok((*DocumentEnvelope)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/documents/{id}/purge", OperationID: "post_documents_id_purge", Tag: "documents",
		Success: noContent(), Errors: commonErrors},

	// conversations
	{Method: http.MethodPost, Path: "/api/v1/search", OperationID: "post_search", Tag: "conversations",
		Request: jsonBody((*SearchRequest)(nil)), Success: ok((*EvidenceSet)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/sessions", OperationID: "get_sessions", Tag: "conversations",
		Success: ok((*ConversationList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/sessions", OperationID: "post_sessions", Tag: "conversations",
		Request: jsonBody((*ConversationCreateRequest)(nil)), Success: created((*Conversation)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/sessions/{id}/messages", OperationID: "get_sessions_id_messages", Tag: "conversations",
		Success: ok((*MessageList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/messages/{messageId}/feedback", OperationID: "post_sessions_id_messages_messageId_feedback", Tag: "conversations",
		Request: jsonBody((*RetrievalFeedbackRequest)(nil)), Success: noContent(), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/sessions/{id}/messages", OperationID: "post_sessions_id_messages", Tag: "conversations",
		Request: jsonBody((*MessageCreateRequest)(nil)), Success: eventStream((*StreamEvent)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/requests/{requestID}/events", OperationID: "get_requests_requestID_events", Tag: "conversations",
		Success: eventStream((*StreamEvent)(nil)), Errors: commonErrors},

	// connectors
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/connectors", OperationID: "get_knowledge_bases_id_connectors", Tag: "connectors",
		Success: ok((*ConnectorList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/connectors", OperationID: "post_knowledge_bases_id_connectors", Tag: "connectors",
		Request: jsonBody((*ConnectorCreateRequest)(nil)), Success: created((*Connector)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/sync/cancel", OperationID: "post_knowledge_bases_id_sync_cancel", Tag: "connectors",
		Success: accepted((*CancelResult)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/connectors/{id}", OperationID: "get_connectors_id", Tag: "connectors",
		Success: ok((*Connector)(nil)), Errors: commonErrors},
	{Method: http.MethodPatch, Path: "/api/v1/connectors/{id}", OperationID: "patch_connectors_id", Tag: "connectors",
		Request: jsonBody((*ConnectorUpdateRequest)(nil)), Success: ok((*Connector)(nil)), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/connectors/{id}", OperationID: "delete_connectors_id", Tag: "connectors",
		Success: noContent(), Errors: deleteErrors},
	{Method: http.MethodPost, Path: "/api/v1/connectors/{id}/sync", OperationID: "post_connectors_id_sync", Tag: "connectors",
		Success: accepted((*ConnectorRun)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/connectors/{id}/cancel", OperationID: "post_connectors_id_cancel", Tag: "connectors",
		Success: accepted((*CancelResult)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/connectors/{id}/permissions", OperationID: "get_connectors_id_permissions", Tag: "connectors",
		Success: ok((*ConnectorPermissions)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/connectors/{id}/runs", OperationID: "get_connectors_id_runs", Tag: "connectors",
		Success: ok((*ConnectorRunList)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/connectors/{id}/runs/{runId}", OperationID: "get_connectors_id_runs_runId", Tag: "connectors",
		Success: ok((*ConnectorRun)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/connectors/{id}/runs/{runId}/retry", OperationID: "post_connectors_id_runs_runId_retry", Tag: "connectors",
		Success: accepted((*ConnectorRun)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/connectors/{id}/runs/{runId}/cancel", OperationID: "post_connectors_id_runs_runId_cancel", Tag: "connectors",
		Success: accepted((*ConnectorRun)(nil)), Errors: commonErrors},

	// projections
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/projection", OperationID: "get_knowledge_bases_id_projection", Tag: "projections",
		Success: ok((*ProjectionState)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/wiki/pages", OperationID: "get_knowledge_bases_id_wiki_pages", Tag: "projections",
		Success: ok((*WikiPageList)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/wiki/runs", OperationID: "get_knowledge_bases_id_wiki_runs", Tag: "projections",
		Success: ok((*WikiRunList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/wiki/rebuild", OperationID: "post_knowledge_bases_id_wiki_rebuild", Tag: "projections",
		Success: accepted((*StartResult)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/knowledge/search", OperationID: "get_knowledge_bases_id_knowledge_search", Tag: "projections",
		Success: ok((*KnowledgeSearchResult)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/wiki/runs/{runId}", OperationID: "get_wiki_runs_runId", Tag: "projections",
		Success: ok((*WikiRunDetail)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/wiki/runs/{runId}/retry", OperationID: "post_wiki_runs_runId_retry", Tag: "projections",
		Success: accepted((*ProjectionRun)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/wiki/pages/{pageId}", OperationID: "get_wiki_pages_pageId", Tag: "projections",
		Success: ok((*WikiPageDetail)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/wiki/pages/{pageId}/versions", OperationID: "get_wiki_pages_pageId_versions", Tag: "projections",
		Success: ok((*WikiVersionList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/wiki/pages/{pageId}/versions/{versionId}/review", OperationID: "post_wiki_pages_pageId_versions_versionId_review", Tag: "projections",
		Request: jsonBody((*WikiReviewRequest)(nil)), Success: ok((*WikiPageVersion)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/wiki/pages/{pageId}/versions/{versionId}/publish", OperationID: "post_wiki_pages_pageId_versions_versionId_publish", Tag: "projections",
		Success: ok((*WikiPage)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/wiki/pages/{pageId}/rollback", OperationID: "post_wiki_pages_pageId_rollback", Tag: "projections",
		Request: jsonBody((*WikiRollbackRequest)(nil)), Success: ok((*WikiPage)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/graph/query", OperationID: "get_knowledge_bases_id_graph_query", Tag: "projections",
		Success: ok((*GraphQueryResult)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/graph/snapshots", OperationID: "get_knowledge_bases_id_graph_snapshots", Tag: "projections",
		Success: ok((*GraphSnapshotList)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/graph/runs", OperationID: "get_knowledge_bases_id_graph_runs", Tag: "projections",
		Success: ok((*GraphRunList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/graph/rebuild", OperationID: "post_knowledge_bases_id_graph_rebuild", Tag: "projections",
		Success: accepted((*StartResult)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/graph/snapshots/{snapshotId}", OperationID: "get_graph_snapshots_snapshotId", Tag: "projections",
		Success: ok((*GraphSnapshotDetail)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/graph/snapshots/{snapshotId}/review", OperationID: "post_graph_snapshots_snapshotId_review", Tag: "projections",
		Request: jsonBody((*GraphReviewRequest)(nil)), Success: ok((*GraphSnapshot)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/graph/runs/{runId}", OperationID: "get_graph_runs_runId", Tag: "projections",
		Success: ok((*GraphRunDetail)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/graph/runs/{runId}/retry", OperationID: "post_graph_runs_runId_retry", Tag: "projections",
		Success: accepted((*ProjectionRun)(nil)), Errors: commonErrors},

	// operations
	{Method: http.MethodGet, Path: "/api/v1/ops/tasks/{key}", OperationID: "get_ops_tasks_key", Tag: "operations",
		Success: ok((*TaskInspection)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/ops/dead-letters", OperationID: "get_ops_dead_letters", Tag: "operations",
		Success: ok((*DeadLetterList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/ops/dead-letters/{id}/retry", OperationID: "post_ops_dead_letters_id_retry", Tag: "operations",
		Success: noContent(), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/ops/dead-letters/{id}/discard", OperationID: "post_ops_dead_letters_id_discard", Tag: "operations",
		Success: noContent(), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/ops/pipelines/{id}/cancel", OperationID: "post_ops_pipelines_id_cancel", Tag: "operations",
		Success: noContent(), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/ops/reconcile", OperationID: "post_ops_reconcile", Tag: "operations",
		Success: ok((*ReconcileReport)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/ops/metrics", OperationID: "get_ops_metrics", Tag: "operations",
		Success: ok(OpsMetrics{}), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/ops/metrics/prometheus", OperationID: "get_ops_metrics_prometheus", Tag: "operations",
		Success: plainText(""), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/ops/quality", OperationID: "get_ops_quality", Tag: "operations",
		Success: ok((*RetrievalQualitySummary)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/ops/quality/eval-runs", OperationID: "post_ops_quality_eval_runs", Tag: "operations",
		Request: jsonBody((*RetrievalEvalRunRequest)(nil)), Success: created((*RetrievalEvalRun)(nil)), Errors: commonErrors},

	// model providers
	{Method: http.MethodGet, Path: "/api/v1/models/catalog", OperationID: "get_models_catalog", Tag: "models",
		Success: ok((*ModelCatalog)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/models/providers", OperationID: "get_models_providers", Tag: "models",
		Success: ok((*ModelProviderList)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/models/providers", OperationID: "post_models_providers", Tag: "models",
		Request: jsonBody((*ModelProviderUpsertRequest)(nil)), Success: created((*ModelProvider)(nil)), Errors: commonErrors},
	{Method: http.MethodPatch, Path: "/api/v1/models/providers/{id}", OperationID: "patch_models_providers_id", Tag: "models",
		Request: jsonBody((*ModelProviderUpsertRequest)(nil)), Success: ok((*ModelProvider)(nil)), Errors: commonErrors},
	{Method: http.MethodDelete, Path: "/api/v1/models/providers/{id}", OperationID: "delete_models_providers_id", Tag: "models",
		Success: noContent(), Errors: deleteErrors},
	{Method: http.MethodPost, Path: "/api/v1/models/providers/{id}/test", OperationID: "post_models_providers_id_test", Tag: "models",
		Success: ok((*ModelProviderTestResult)(nil)), Errors: commonErrors},

	// embedding migration
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/embedding-migration", OperationID: "post_knowledge_bases_id_embedding_migration", Tag: "embedding-migration",
		Request: jsonBody((*EmbeddingMigrationStartRequest)(nil)), Success: accepted((*EmbeddingMigrationV1)(nil)), Errors: commonErrors},
	{Method: http.MethodGet, Path: "/api/v1/knowledge-bases/{id}/embedding-migration", OperationID: "get_knowledge_bases_id_embedding_migration", Tag: "embedding-migration",
		Success: ok((*EmbeddingMigrationV1)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/cancel", OperationID: "post_knowledge_bases_id_embedding_migration_migrationId_cancel", Tag: "embedding-migration",
		Success: ok((*EmbeddingMigrationV1)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/cutover", OperationID: "post_knowledge_bases_id_embedding_migration_migrationId_cutover", Tag: "embedding-migration",
		Success: ok((*EmbeddingMigrationV1)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/rollback", OperationID: "post_knowledge_bases_id_embedding_migration_migrationId_rollback", Tag: "embedding-migration",
		Success: ok((*EmbeddingMigrationV1)(nil)), Errors: commonErrors},
	{Method: http.MethodPost, Path: "/api/v1/knowledge-bases/{id}/embedding-migration/{migrationId}/finalize", OperationID: "post_knowledge_bases_id_embedding_migration_migrationId_finalize", Tag: "embedding-migration",
		Success: ok((*EmbeddingMigrationV1)(nil)), Errors: commonErrors},
}
