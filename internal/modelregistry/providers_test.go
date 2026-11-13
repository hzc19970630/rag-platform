package modelregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

type fakeCipher struct{}

func (fakeCipher) Encrypt(plaintext []byte) (string, error) {
	return "sealed:" + string(plaintext), nil
}

func (fakeCipher) Decrypt(envelope string) ([]byte, error) {
	return []byte(strings.TrimPrefix(envelope, "sealed:")), nil
}

func providerInput(name string, entries ...CatalogEntryInput) ProviderInput {
	return ProviderInput{
		Name: name, Kind: ProviderKindOpenAICompatible, BaseURL: "https://llm.example.com/v1",
		APIKey: stringPtr("sk-test"), Models: entries,
	}
}

func chatEntry(name string) CatalogEntryInput {
	return CatalogEntryInput{Model: name, Capability: string(model.CapabilityChat)}
}

func stringPtr(value string) *string { return &value }

func TestCreateProviderValidatesAndHidesKey(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{Cipher: fakeCipher{}})
	provider, err := registry.CreateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, providerInput("primary", chatEntry("m1")))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if !provider.APIKeySet || provider.Name != "primary" || len(provider.Models) != 1 {
		t.Fatalf("unexpected provider view: %+v", provider)
	}
	listed, err := registry.ListProviders(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(listed) != 1 || listed[0].Models[0].Model != "m1" {
		t.Fatalf("unexpected list: %+v", listed)
	}
	route, err := registry.ProviderForModel(context.Background(), "tenant-1", "m1")
	if err != nil {
		t.Fatalf("route model: %v", err)
	}
	if route.APIKey != "sk-test" || route.ProviderID != provider.ID {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestProviderValidationRejectsBadInput(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{Models: catalogModels(nil), Cipher: fakeCipher{}})
	cases := []struct {
		name  string
		input ProviderInput
	}{
		{"unknown kind", ProviderInput{Name: "p", Kind: "azure_openai", BaseURL: "https://x.example.com"}},
		{"bad url", ProviderInput{Name: "p", Kind: ProviderKindOpenAICompatible, BaseURL: "ftp://x.example.com"}},
		{"credential in url", ProviderInput{Name: "p", Kind: ProviderKindOpenAICompatible, BaseURL: "https://user:pass@x.example.com"}},
		{"embedding without dimension", ProviderInput{Name: "p", Kind: ProviderKindOpenAICompatible, BaseURL: "https://x.example.com", Models: []CatalogEntryInput{{Model: "e", Capability: "embedding"}}}},
		{"unknown capability", ProviderInput{Name: "p", Kind: ProviderKindOpenAICompatible, BaseURL: "https://x.example.com", Models: []CatalogEntryInput{{Model: "e", Capability: "vision"}}}},
		{"duplicate model in request", ProviderInput{Name: "p", Kind: ProviderKindOpenAICompatible, BaseURL: "https://x.example.com", Models: []CatalogEntryInput{chatEntry("a"), chatEntry("a")}}},
		{"deployment model name", ProviderInput{Name: "p", Kind: ProviderKindOpenAICompatible, BaseURL: "https://x.example.com", Models: []CatalogEntryInput{chatEntry("chat-a")}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := registry.CreateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, testCase.input)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestUpdateProviderKeepsBoundEntries(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(nil),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
		Cipher:   fakeCipher{},
	})
	provider, err := registry.CreateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, providerInput("primary", chatEntry("tenant-chat")))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	seedKnowledgeBase(t, db, "kb-1", "tenant-1", "tenant-chat")

	updated := providerInput("primary", chatEntry("tenant-chat"))
	updated.Models[0].Version = "2026-01"
	if _, err := registry.UpdateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, provider.ID, updated); err != nil {
		t.Fatalf("version-only update should succeed: %v", err)
	}
	removed := providerInput("primary")
	if _, err := registry.UpdateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, provider.ID, removed); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("expected conflict removing a bound model, got %v", err)
	}
	if err := registry.DeleteProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, provider.ID); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("expected conflict deleting a provider with a bound model, got %v", err)
	}
}

func TestTenantModelResolutionAndDegrade(t *testing.T) {
	db := openRegistryTestDB(t)
	healthy := true
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(nil),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
		Cipher:   fakeCipher{},
		// A one-nanosecond TTL keeps the degrade assertion from reading the
		// cached healthy verdict from the previous call.
		HealthTTL: time.Nanosecond,
		ProbeProvider: func(context.Context, ProviderRoute) error {
			if healthy {
				return nil
			}
			return errors.New("provider down")
		},
	})
	provider, err := registry.CreateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, providerInput("primary", chatEntry("tenant-chat")))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	seedKnowledgeBase(t, db, "kb-1", "tenant-1", "tenant-chat")

	resolved, err := registry.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-1", model.CapabilityChat)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Effective.Model != "tenant-chat" || resolved.ProviderID != provider.ID || resolved.Degraded {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}

	healthy = false
	resolved, err = registry.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-1", model.CapabilityChat)
	if err != nil {
		t.Fatalf("resolve degraded: %v", err)
	}
	if resolved.Effective.Model != "chat-default" || !resolved.Degraded {
		t.Fatalf("expected fallback to the deployment default, got %+v", resolved)
	}
}

func TestProviderCatalogIsTenantScoped(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{Cipher: fakeCipher{}})
	provider, err := registry.CreateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, providerInput("primary", chatEntry("m1")))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := registry.ProviderForModel(context.Background(), "tenant-2", "m1"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected not found across tenants, got %v", err)
	}
	if err := registry.DeleteProvider(context.Background(), "tenant-2", Actor{ID: "intruder"}, provider.ID); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected cross-tenant delete to be not found, got %v", err)
	}
	if _, err := registry.TestProvider(context.Background(), "tenant-2", Actor{ID: "intruder"}, provider.ID); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected cross-tenant test to be not found, got %v", err)
	}
}

func TestUpdateProviderRejectsCapabilityChangeWhileBound(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{Cipher: fakeCipher{}})
	provider, err := registry.CreateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, providerInput("primary", chatEntry("m1")))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-1", TenantID: "tenant-1", OwnerID: "owner", Name: "kb", ACLVersion: 1,
		RetrievalConfig: "{}", IndexingStrategy: "{}", ChatModel: "m1", ChatModelVersion: "deployment",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	changed := providerInput("primary", CatalogEntryInput{Model: "m1", Capability: string(model.CapabilityRerank)})
	if _, err := registry.UpdateProvider(context.Background(), "tenant-1", Actor{ID: "admin"}, provider.ID, changed); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("expected conflict changing a bound capability, got %v", err)
	}
}
