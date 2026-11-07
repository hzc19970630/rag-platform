package views

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/datasource"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

// TestResponseViewsPreserveWireShape serializes a zero-valued source and its
// mapped contract and requires identical JSON keys. A column or view field
// that the mapping forgets shows up here, before it reaches a client.
func TestResponseViewsPreserveWireShape(t *testing.T) {
	cases := []struct {
		name   string
		source any
		target any
	}{
		{"document", models.Document{}, Document(models.Document{})},
		{"document version", models.DocumentVersion{}, DocumentVersion(models.DocumentVersion{})},
		{"pipeline run", models.PipelineRun{}, PipelineRun(models.PipelineRun{})},
		{"stage run", models.StageRun{}, StageRun(models.StageRun{})},
		{"knowledge base", models.KnowledgeBase{}, KnowledgeBase(models.KnowledgeBase{})},
		{"knowledge base member", models.KnowledgeBaseMember{}, KnowledgeBaseMember(models.KnowledgeBaseMember{})},
		{"conversation", models.Conversation{}, Conversation(models.Conversation{})},
		{"message", models.Message{}, Message(models.Message{})},
		{"audit event", models.AuditLog{}, AuditEvent(models.AuditLog{})},
		{"projection run", models.ProjectionRun{}, ProjectionRun(models.ProjectionRun{})},
		{"projection stage run", models.ProjectionStageRun{}, ProjectionStageRun(models.ProjectionStageRun{})},
		{"wiki page", models.WikiPage{}, WikiPage(models.WikiPage{})},
		{"wiki version", models.WikiPageVersion{}, WikiPageVersion(models.WikiPageVersion{})},
		{"wiki block", models.WikiBlock{}, WikiBlock(models.WikiBlock{})},
		{"wiki source", models.WikiPageSource{}, WikiPageSource(models.WikiPageSource{})},
		{"graph snapshot", models.GraphSnapshot{}, GraphSnapshot(models.GraphSnapshot{})},
		{"graph alias", models.GraphEntityAlias{}, GraphEntityAlias(models.GraphEntityAlias{})},
		{"task inbox", models.TaskInbox{}, TaskInbox(models.TaskInbox{})},
		{"outbox event", models.OutboxEvent{}, OutboxEvent(models.OutboxEvent{})},
		{"task dead letter", models.TaskDeadLetter{}, TaskDeadLetter(models.TaskDeadLetter{})},
		{"document view", ingestion.DocumentView{}, DocumentView(ingestion.DocumentView{})},
		{"document summary", ingestion.DocumentSummary{}, DocumentSummary(ingestion.DocumentSummary{})},
		{"document page", ingestion.DocumentPage{}, DocumentPage(ingestion.DocumentPage{})},
		{"chunk", ingestion.ChunkView{}, Chunk(ingestion.ChunkView{})},
		{"chunk page", ingestion.ChunkPage{}, ChunkPage(ingestion.ChunkPage{})},
		{"document preview", ingestion.DocumentPreview{}, DocumentPreview(ingestion.DocumentPreview{})},
		{"document status", ingestion.DocumentStatusView{}, DocumentStatus(ingestion.DocumentStatusView{})},
		{"document version view", ingestion.DocumentVersionView{}, DocumentVersionView(ingestion.DocumentVersionView{})},
		{"document version page", ingestion.DocumentVersionPage{}, DocumentVersionPage(ingestion.DocumentVersionPage{})},
		{"reconcile report", ingestion.ReconcileReport{}, ReconcileReport(ingestion.ReconcileReport{})},
		{"evidence set", retrieval.EvidenceSet{}, EvidenceSet(retrieval.EvidenceSet{})},
		{"connector", datasource.ConnectorView{}, Connector(datasource.ConnectorView{})},
		{"connector run", datasource.RunView{}, ConnectorRun(datasource.RunView{})},
		{"connector permissions", datasource.PermissionMappingView{}, ConnectorPermissions(datasource.PermissionMappingView{})},
		{"api key", identity.APIKeyView{}, APIKey(identity.APIKeyView{})},
		{"tenant member", authz.TenantMemberView{}, TenantMember(authz.TenantMemberView{})},
		{"citation", conversation.CitationView{}, Citation(conversation.CitationView{})},
		{"projection state", knowledgeprojection.ProjectionStatus{}, ProjectionState(knowledgeprojection.ProjectionStatus{})},
		{"wiki page summary", knowledgeprojection.PageSummary{}, WikiPageSummary(knowledgeprojection.PageSummary{})},
		{"wiki page detail", knowledgeprojection.PageDetail{}, WikiPageDetail(knowledgeprojection.PageDetail{})},
		{"wiki run detail", knowledgeprojection.RunDetail{}, WikiRunDetail(knowledgeprojection.RunDetail{})},
		{"knowledge search", knowledgeprojection.SearchResult{}, KnowledgeSearchResult(knowledgeprojection.SearchResult{})},
		{"graph query", knowledgeprojection.GraphQueryResult{}, GraphQueryResult(knowledgeprojection.GraphQueryResult{})},
		{"graph snapshot detail", knowledgeprojection.GraphSnapshotDetail{}, GraphSnapshotDetail(knowledgeprojection.GraphSnapshotDetail{})},
		{"graph run detail", knowledgeprojection.GraphRunDetail{}, GraphRunDetail(knowledgeprojection.GraphRunDetail{})},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := marshalJSON(t, testCase.source)
			target := marshalJSON(t, testCase.target)
			// A nil slice and an empty slice are the same collection; views
			// normalize list fields to arrays so the wire matches the schema.
			if !sameWireShape(source, target) {
				t.Fatalf("wire shape differs:\nsource %v\nview   %v", source, target)
			}
		})
	}
}

// TestResponseViewsPreserveWireValues fills every exported source field through
// JSON round-tripping, then requires the view to produce the same document.
func TestResponseViewsPreserveWireValues(t *testing.T) {
	deletedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	document := models.Document{
		ID: "doc", TenantID: "tenant", KnowledgeBaseID: "kb", OwnerID: "owner",
		Filename: "report.pdf", MimeType: "application/pdf", Status: "ready",
		LatestVersionID: "version", DeletedAt: &deletedAt, DeletedBy: "operator",
		PurgeAfter: &deletedAt, RestoredAt: &deletedAt, CreatedAt: deletedAt, UpdatedAt: deletedAt,
	}
	version := models.DocumentVersion{
		ID: "version", DocumentID: "doc", TenantID: "tenant", KnowledgeBaseID: "kb",
		Version: 3, ContentHash: "hash", SizeBytes: 4096, Status: "ready", ACLVersion: 7,
		ErrorCode: "", ErrorMessage: "", CreatedAt: deletedAt, UpdatedAt: deletedAt,
	}
	knowledgeBase := models.KnowledgeBase{
		ID: "kb", TenantID: "tenant", OwnerID: "owner", Name: "Handbook",
		Description: "team handbook", EmbeddingModel: "embed", EmbeddingDimension: 1024,
		ChatModel: "chat", RerankModel: "rerank", ACLVersion: 2,
		RetrievalConfig: `{"topK":8}`, IndexingStrategy: `{"vectorEnabled":true}`,
		CreatedAt: deletedAt, UpdatedAt: deletedAt,
	}
	cases := []struct {
		name   string
		source any
		target any
	}{
		{"document", document, Document(document)},
		{"document version", version, DocumentVersion(version)},
		{"knowledge base", knowledgeBase, KnowledgeBase(knowledgeBase)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := marshalJSON(t, testCase.source)
			target := marshalJSON(t, testCase.target)
			if !reflect.DeepEqual(source, target) {
				t.Fatalf("wire values differ:\nsource %v\nview   %v", source, target)
			}
		})
	}
}

func TestStreamEventKeepsSSEContract(t *testing.T) {
	event := conversation.Event{
		EventID:   "req-1:3",
		RequestID: "req-1",
		Sequence:  3,
		Type:      "delta",
		Data:      map[string]any{"text": "hello"},
	}
	view := StreamEvent(event)
	if view.EventID != event.EventID || view.RequestID != event.RequestID ||
		view.Sequence != event.Sequence || view.Type != event.Type {
		t.Fatalf("stream envelope changed: %#v", view)
	}
	if !reflect.DeepEqual(view.Data, event.Data) {
		t.Fatalf("stream data = %#v, want %#v", view.Data, event.Data)
	}
}

func TestAuthConfigConvertsSecondsToHours(t *testing.T) {
	view := AuthConfig(true, "https://issuer", true, "rag_session", 7200)
	want := httpapi.AuthConfig{
		OIDCEnabled: true, OIDCIssuer: "https://issuer", DevAuthEnabled: true,
		SessionCookie: "rag_session", SessionTTLHours: 2,
	}
	if view != want {
		t.Fatalf("auth config = %#v, want %#v", view, want)
	}
}

func TestPrincipalAndScopeCloneSlices(t *testing.T) {
	principal := authz.Principal{
		SubjectID: "user", TenantID: "tenant", Capabilities: []string{"read"},
		CredentialType: "session", CredentialID: "credential",
	}
	scope := authz.AuthorizationScope{
		SubjectID: "user", TenantID: "tenant",
		KnowledgeBaseIDs: []string{"kb"}, ResourceIDs: []string{"doc"},
		Capabilities: []string{"read"}, ACLVersion: "3",
	}
	view := Me(principal, scope, true)
	principal.Capabilities[0] = "write"
	scope.KnowledgeBaseIDs[0] = "other"
	if view.Principal.Capabilities[0] != "read" {
		t.Fatal("principal capabilities must be copied, not aliased")
	}
	if view.Scope.KnowledgeBaseIDs[0] != "kb" {
		t.Fatal("scope knowledge base ids must be copied, not aliased")
	}
	if !view.DevelopmentIdentity || view.Scope.ACLVersion != "3" {
		t.Fatalf("me response = %#v", view)
	}
}

func TestTaskInspectionKeepsOptionalSections(t *testing.T) {
	task := ingestion.Task{Stage: "parse"}
	view := TaskInspection(ingestion.TaskInspection{
		Task:       task,
		Inbox:      &models.TaskInbox{IdempotencyKey: "key"},
		Stage:      &models.StageRun{ID: 7},
		Outbox:     []models.OutboxEvent{{ID: 1}},
		DeadLetter: &models.TaskDeadLetter{ID: 9},
	})
	if !reflect.DeepEqual(view.Task, task) {
		t.Fatalf("task = %#v, want %#v", view.Task, task)
	}
	if view.Inbox == nil || view.Inbox.IdempotencyKey != "key" {
		t.Fatalf("inbox = %#v", view.Inbox)
	}
	if view.Stage == nil || view.Stage.ID != 7 {
		t.Fatalf("stage = %#v", view.Stage)
	}
	if view.Pipeline != nil {
		t.Fatalf("pipeline = %#v, want nil", view.Pipeline)
	}
	if len(view.Outbox) != 1 || view.Outbox[0].ID != 1 {
		t.Fatalf("outbox = %#v", view.Outbox)
	}
	if view.DeadLetter == nil || view.DeadLetter.ID != 9 {
		t.Fatalf("dead letter = %#v", view.DeadLetter)
	}
}

func marshalJSON(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %T: %v", value, err)
	}
	return decoded
}

func sameWireShape(source, target any) bool {
	if source == nil || target == nil {
		return emptyCollection(source) && emptyCollection(target)
	}
	switch typed := source.(type) {
	case map[string]any:
		other, ok := target.(map[string]any)
		if !ok || len(typed) != len(other) {
			return false
		}
		for key, value := range typed {
			otherValue, ok := other[key]
			if !ok || !sameWireShape(value, otherValue) {
				return false
			}
		}
		return true
	case []any:
		other, ok := target.([]any)
		if !ok || len(typed) != len(other) {
			return false
		}
		for index := range typed {
			if !sameWireShape(typed[index], other[index]) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func emptyCollection(value any) bool {
	if value == nil {
		return true
	}
	if items, ok := value.([]any); ok {
		return len(items) == 0
	}
	return false
}
