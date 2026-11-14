package views

import (
	"testing"

	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/datasource"
	"github.com/hzc19970630/rag-platform/internal/identity"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/knowledgeprojection"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/tenant"
)

// These mappers are pure projections; exercising their collection and input
// paths keeps wire-shape regressions visible without a live stack.
func TestCollectionAndInputMappers(t *testing.T) {
	config := httpapi.ConnectorConfig{ManifestURL: "https://example.com/m.json"}
	credential := httpapi.CredentialInput{Type: "bearer", Token: "token"}

	if items := Connectors([]datasource.ConnectorView{{ID: "conn-1"}}); len(items) != 1 || items[0].ID != "conn-1" {
		t.Fatalf("connectors = %+v", items)
	}
	if items := ConnectorRuns([]datasource.RunView{{ID: "run-1"}}); len(items) != 1 || items[0].ID != "run-1" {
		t.Fatalf("connector runs = %+v", items)
	}
	if input := CreateConnectorInput(httpapi.ConnectorCreateRequest{Name: "c", Config: config, Credential: &credential}); input.Config.ManifestURL != config.ManifestURL || input.Credential == nil {
		t.Fatalf("create connector input = %+v", input)
	}
	name := "c"
	update := UpdateConnectorInput(httpapi.ConnectorUpdateRequest{Name: &name, Config: &config, Credential: nil})
	if update.Config == nil || update.Config.ManifestURL != config.ManifestURL || update.Credential != nil {
		t.Fatalf("update connector input = %+v", update)
	}
	_ = ConnectorPermissions(datasource.PermissionMappingView{})

	if items := Conversations([]models.Conversation{{ID: "conv-1"}}); len(items) != 1 || items[0].ID != "conv-1" {
		t.Fatalf("conversations = %+v", items)
	}
	if items := Citations([]conversation.CitationView{{ChunkID: "chunk-1"}}); len(items) != 1 || items[0].ChunkID != "chunk-1" {
		t.Fatalf("citations = %+v", items)
	}
	if items := MessageViews([]conversation.MessageView{}); len(items) != 0 || items == nil {
		t.Fatalf("message views = %#v", items)
	}
	_ = CreateConversationInput(httpapi.ConversationCreateRequest{KnowledgeBaseID: "kb-1"})
	_ = AskInput("conv-1", httpapi.MessageCreateRequest{Query: "q"}, "req-1")

	_ = UploadResult(ingestion.UploadResult{})
	_ = DocumentEnvelope(models.Document{ID: "doc-1"})
	_ = PreviewBlock(ingestion.PreviewBlock{})
	_ = EmbeddingMigration(models.EmbeddingMigration{ID: "mig-1"})

	if items := TenantMembers([]authz.TenantMemberView{{UserID: "user-1"}}); len(items) != 1 || items[0].UserID != "user-1" {
		t.Fatalf("tenant members = %+v", items)
	}
	_ = TenantOverview(tenant.Overview{ID: "tenant-1"})
	quota := TenantQuota(tenantquota.Effective{MaxDocuments: tenantquota.Field{Value: 5, Overridden: true}})
	if quota.MaxDocuments.Source != "tenant" || quota.MaxDocuments.Unlimited {
		t.Fatalf("tenant quota = %+v", quota.MaxDocuments)
	}
	unlimited := TenantQuota(tenantquota.Effective{MaxDocuments: tenantquota.Field{Value: 0}})
	if unlimited.MaxDocuments.Source != "deployment" || !unlimited.MaxDocuments.Unlimited {
		t.Fatalf("unlimited quota = %+v", unlimited.MaxDocuments)
	}
	if items := AuditEvents([]models.AuditLog{{ID: 1}}); len(items) != 1 || items[0].ID != 1 {
		t.Fatalf("audit events = %+v", items)
	}
	_ = User(models.User{ID: "user-1"})
	if keys := APIKeys([]identity.APIKeyView{{ID: "key-1"}}); len(keys) != 1 || keys[0].ID != "key-1" {
		t.Fatalf("api keys = %+v", keys)
	}
	_ = CreateAPIKeyInput(httpapi.APIKeyCreateRequest{Name: "key"})
	_ = DevLogin(identity.SessionResult{})

	if items := KnowledgeBases([]models.KnowledgeBase{{ID: "kb-1", PresetName: "team-pack"}}); len(items) != 1 || items[0].PresetName != "team-pack" {
		t.Fatalf("knowledge bases = %+v", items)
	}
	if members := KnowledgeBaseMembers([]models.KnowledgeBaseMember{{UserID: "user-1"}}); len(members) != 1 {
		t.Fatalf("knowledge base members = %+v", members)
	}
	_ = CreateKnowledgeBaseInput(httpapi.KnowledgeBaseCreateRequest{Name: "kb", PresetName: "team-pack"})
	_ = UpdateKnowledgeBaseInput(httpapi.KnowledgeBaseUpdateRequest{})
	_ = AddKnowledgeBaseMemberInput(httpapi.KnowledgeBaseMemberCreateRequest{UserID: "user-1"})
	_ = IndexingStrategyInput(httpapi.IndexingStrategy{VectorEnabled: true})
	topK := 5
	_ = RetrievalOverride(retrieval.ParamsOverride{TopK: &topK})
	_ = ParamsOverride(httpapi.RetrievalConfigOverride{TopK: &topK})

	if providers := ModelProviders([]modelregistry.Provider{{ID: "provider-1", Models: []modelregistry.CatalogEntry{{Model: "m"}}}}); len(providers) != 1 || len(providers[0].Models) != 1 {
		t.Fatalf("model providers = %+v", providers)
	}
	if catalog := ModelCatalog([]modelregistry.CatalogModel{{Model: "m"}}); len(catalog.Items) != 1 {
		t.Fatalf("model catalog = %+v", catalog)
	}
	_ = ModelProviderInput(httpapi.ModelProviderUpsertRequest{Name: "p"})
	_ = ModelProviderTestResult(modelregistry.ProviderTestResult{Healthy: true})

	if items := TaskDeadLetters([]models.TaskDeadLetter{{IdempotencyKey: "k"}}); len(items) != 1 {
		t.Fatalf("dead letters = %+v", items)
	}
	if items := WikiPageSummaries([]knowledgeprojection.PageSummary{{Page: models.WikiPage{ID: "page-1"}}}); len(items) != 1 {
		t.Fatalf("wiki page summaries = %+v", items)
	}
	_ = WikiPageVersions([]models.WikiPageVersion{{ID: "wv-1"}})
}
