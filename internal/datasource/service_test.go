package datasource

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/secrets"
)

func testService(t *testing.T) *Service {
	t.Helper()
	cipher, err := secrets.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	return &Service{cipher: cipher, maxItemsLimit: hardMaxItemsPerRun}
}

func TestNormalizeConfigAppliesDefaults(t *testing.T) {
	service := testService(t)
	config, err := HTTPManifestConnector{}.NormalizeConfig(ConnectorConfig{ManifestURL: "https://example.com/manifest.json"}, service.maxItemsLimit)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if config.MaxItemsPerRun != defaultMaxItemsPerRun {
		t.Fatalf("max items default: %d", config.MaxItemsPerRun)
	}
	if config.ACLMode != ACLModeKnowledgeBase {
		t.Fatalf("acl mode default: %q", config.ACLMode)
	}
	if config.RequestsPerSecond != 0 {
		t.Fatalf("requests per second default must stay unset: %v", config.RequestsPerSecond)
	}
}

func TestNormalizeConfigRejectsInvalidValues(t *testing.T) {
	service := testService(t)
	cases := []ConnectorConfig{
		{ManifestURL: ""},
		{ManifestURL: "ftp://example.com/m.json"},
		{ManifestURL: "https://user:pass@example.com/m.json"},
		{ManifestURL: "https://example.com/m.json", RequestsPerSecond: -1},
		{ManifestURL: "https://example.com/m.json", RequestsPerSecond: 51},
		{ManifestURL: "https://example.com/m.json", MaxItemsPerRun: -1},
		{ManifestURL: "https://example.com/m.json", MaxItemsPerRun: hardMaxItemsPerRun + 1},
		{ManifestURL: "https://example.com/m.json", ACLMode: "document"},
	}
	for _, input := range cases {
		if _, err := (HTTPManifestConnector{}).NormalizeConfig(input, service.maxItemsLimit); err == nil {
			t.Fatalf("expected %+v to be rejected", input)
		}
	}
}

func TestNormalizeConfigTrimsValues(t *testing.T) {
	service := testService(t)
	config, err := HTTPManifestConnector{}.NormalizeConfig(ConnectorConfig{
		ManifestURL: "  https://example.com/manifest.json  ", RequestsPerSecond: 5,
		MaxItemsPerRun: 25, ACLMode: " knowledge_base ",
	}, service.maxItemsLimit)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if config.ManifestURL != "https://example.com/manifest.json" || config.ACLMode != ACLModeKnowledgeBase {
		t.Fatalf("values were not normalized: %+v", config)
	}
}

func TestSealCredentialRoundTrip(t *testing.T) {
	service := testService(t)
	if sealed, err := service.sealCredential(nil); err != nil || sealed != "" {
		t.Fatalf("nil credential must stay unset: %q %v", sealed, err)
	}
	if sealed, err := service.sealCredential(&CredentialInput{Type: "none"}); err != nil || sealed != "" {
		t.Fatalf("none credential must stay unset: %q %v", sealed, err)
	}
	sealed, err := service.sealCredential(&CredentialInput{Type: "Bearer", Token: "secret-token"})
	if err != nil {
		t.Fatalf("seal bearer: %v", err)
	}
	if !strings.HasPrefix(sealed, "v2:legacy:") || strings.Contains(sealed, "secret-token") {
		t.Fatalf("credential must be sealed, got %q", sealed)
	}
	raw, err := service.cipher.Decrypt(sealed)
	if err != nil {
		t.Fatalf("decrypt sealed credential: %v", err)
	}
	var credential Credentials
	if err := json.Unmarshal(raw, &credential); err != nil {
		t.Fatalf("decode credential: %v", err)
	}
	if credential.Type != "bearer" || credential.Token != "secret-token" {
		t.Fatalf("unexpected credential: %+v", credential)
	}
}

func TestSealCredentialValidatesShape(t *testing.T) {
	service := testService(t)
	cases := []CredentialInput{
		{Type: "bearer"},
		{Type: "basic", Username: "user"},
		{Type: "basic", Password: "pass"},
		{Type: "oauth"},
	}
	for _, input := range cases {
		if _, err := service.sealCredential(&input); err == nil {
			t.Fatalf("expected %+v to be rejected", input)
		}
	}
	sealed, err := service.sealCredential(&CredentialInput{Type: "basic", Username: "user", Password: "pass"})
	if err != nil || sealed == "" {
		t.Fatalf("seal basic credential: %q %v", sealed, err)
	}
}

func TestStableIdentifiersAreContentScoped(t *testing.T) {
	documentID := stableDocumentID("conn-1", "files/a.md")
	if documentID != stableDocumentID("conn-1", "files/a.md") {
		t.Fatalf("document id must be deterministic")
	}
	if documentID == stableDocumentID("conn-2", "files/a.md") || documentID == stableDocumentID("conn-1", "files/b.md") {
		t.Fatalf("document id must scope connector and external id")
	}
	if !strings.HasPrefix(documentID, "doc-") || len(documentID) != len("doc-")+24 {
		t.Fatalf("unexpected document id shape: %q", documentID)
	}

	first := stableSyncKey("conn-1", "files/a.md", "hash-1")
	if first != stableSyncKey("conn-1", "files/a.md", "hash-1") {
		t.Fatalf("sync key must be deterministic")
	}
	if first == stableSyncKey("conn-1", "files/a.md", "hash-2") {
		t.Fatalf("sync key must change with content")
	}
	if !strings.HasPrefix(first, "connector:") {
		t.Fatalf("unexpected sync key shape: %q", first)
	}
}

func TestDecodeConfigDefaultsAndInvalidJSON(t *testing.T) {
	if got := decodeConfig(""); got.ACLMode != ACLModeKnowledgeBase {
		t.Fatalf("empty config must default the ACL mode: %+v", got)
	}
	if got := decodeConfig("not-json"); got.ACLMode != ACLModeKnowledgeBase {
		t.Fatalf("invalid config must default the ACL mode: %+v", got)
	}
	config := decodeConfig(`{"manifestUrl":"https://example.com/m.json","aclMode":""}`)
	if config.ManifestURL != "https://example.com/m.json" || config.ACLMode != ACLModeKnowledgeBase {
		t.Fatalf("unexpected decoded config: %+v", config)
	}
}

func TestViewsHideCredentialMaterial(t *testing.T) {
	connector := models.Connector{
		ID: "conn-1", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", Name: "docs", Type: TypeHTTPManifest,
		Config:        `{"manifestUrl":"https://example.com/m.json","maxItemsPerRun":500,"aclMode":"knowledge_base"}`,
		CredentialRef: "v1:sealed-material", Enabled: true, SyncIntervalSeconds: 60,
	}
	view := connectorView(connector)
	if !view.HasCredential {
		t.Fatalf("view must report the credential presence")
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(encoded), "sealed-material") || strings.Contains(string(encoded), "credentialRef") {
		t.Fatalf("view must not expose credential material: %s", encoded)
	}

	now := time.Now()
	run := models.SyncRun{ID: "sync-1", Status: StatusSucceeded, Cursor: "not-json", FinishedAt: &now}
	runView := runView(run)
	if string(runView.Cursor) != "{}" {
		t.Fatalf("invalid cursor must be reported empty: %s", runView.Cursor)
	}
}

func TestIsUniqueViolationText(t *testing.T) {
	if isUniqueViolation(nil) {
		t.Fatalf("nil must not be a unique violation")
	}
	if !isUniqueViolation(errString("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)")) {
		t.Fatalf("postgres unique violation not detected")
	}
	if isUniqueViolation(errString("connection refused")) {
		t.Fatalf("unrelated error reported as unique violation")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc" {
		t.Fatalf("unexpected truncate: %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("short value must be unchanged: %q", got)
	}
	if got := truncate("", 0); got != "" {
		t.Fatalf("empty value must be unchanged: %q", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
