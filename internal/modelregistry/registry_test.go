package modelregistry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func isValidation(err error) bool {
	_, ok := apperror.AsValidation(err)
	return ok
}

func openRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "registry.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&models.KnowledgeBase{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func seedKnowledgeBase(t *testing.T, db *gorm.DB, id, tenantID, chatModel string) {
	t.Helper()
	now := time.Now().UTC()
	row := models.KnowledgeBase{
		ID: id, TenantID: tenantID, OwnerID: "owner", Name: id, ACLVersion: 1,
		RetrievalConfig: "{}", IndexingStrategy: "{}",
		ChatModel: chatModel, ChatModelVersion: versionOf(chatModel),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
}

func versionOf(name string) string {
	if name == "" {
		return ""
	}
	return "v-" + name
}

func catalogModels(probes map[string]func(context.Context) error) []Model {
	chat := func(name string) Model {
		return Model{Name: name, Provider: "remote", Version: "v-" + name, Capabilities: []model.Capability{model.CapabilityChat}, Probe: probes[name]}
	}
	return []Model{
		chat("chat-a"),
		chat("chat-b"),
		chat("chat-default"),
		{Name: "embed-1024", Provider: "remote", Version: "v-embed-1024", Capabilities: []model.Capability{model.CapabilityEmbedding}},
	}
}

func newTestRegistry(t *testing.T, db *gorm.DB, options Options) *Registry {
	t.Helper()
	registry, err := New(db, options)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func TestNewValidatesCatalog(t *testing.T) {
	db := openRegistryTestDB(t)
	chat := model.CapabilityChat
	cases := []struct {
		name    string
		options Options
		want    string
	}{
		{
			name:    "missing name",
			options: Options{Models: []Model{{Capabilities: []model.Capability{chat}}}},
			want:    "model name is required",
		},
		{
			name:    "no capability",
			options: Options{Models: []Model{{Name: "chat-a"}}},
			want:    "has no capability",
		},
		{
			name:    "duplicate",
			options: Options{Models: []Model{{Name: "chat-a", Capabilities: []model.Capability{chat}}, {Name: "chat-a", Capabilities: []model.Capability{chat}}}},
			want:    "duplicate model",
		},
		{
			name:    "unknown default",
			options: Options{Models: catalogModels(nil), Defaults: map[model.Capability]string{chat: "missing"}},
			want:    "is not registered",
		},
		{
			name:    "default lacks capability",
			options: Options{Models: catalogModels(nil), Defaults: map[model.Capability]string{chat: "embed-1024"}},
			want:    "is not registered",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(db, testCase.options); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("expected error containing %q, got %v", testCase.want, err)
			}
		})
	}
}

func TestResolveBindingValidatesCapabilityAndDefaults(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(nil),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})

	defaultRef, err := registry.ResolveBinding(model.CapabilityChat, "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if defaultRef.Model != "chat-default" || defaultRef.Version != "v-chat-default" || defaultRef.Provider != "remote" {
		t.Fatalf("unexpected default ref: %+v", defaultRef)
	}

	bound, err := registry.ResolveBinding(model.CapabilityChat, " chat-a ")
	if err != nil {
		t.Fatalf("resolve bound: %v", err)
	}
	if bound.Model != "chat-a" || bound.Version != "v-chat-a" {
		t.Fatalf("unexpected bound ref: %+v", bound)
	}

	if _, err := registry.ResolveBinding(model.CapabilityChat, "embed-1024"); !isValidation(err) {
		t.Fatalf("expected validation error for wrong capability, got %v", err)
	}
	if _, err := registry.ResolveBinding(model.CapabilityRerank, "chat-a"); !isValidation(err) {
		t.Fatalf("expected validation error for unregistered capability, got %v", err)
	}
}

func TestTwoKnowledgeBasesRouteToDifferentChatModels(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-a", "tenant-1", "chat-a")
	seedKnowledgeBase(t, db, "kb-b", "tenant-1", "chat-b")
	seedKnowledgeBase(t, db, "kb-default", "tenant-1", "")
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(nil),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})

	chatA, err := registry.ChatModel(context.Background(), "tenant-1", "kb-a")
	if err != nil {
		t.Fatalf("resolve kb-a: %v", err)
	}
	chatB, err := registry.ChatModel(context.Background(), "tenant-1", "kb-b")
	if err != nil {
		t.Fatalf("resolve kb-b: %v", err)
	}
	fallback, err := registry.ChatModel(context.Background(), "tenant-1", "kb-default")
	if err != nil {
		t.Fatalf("resolve kb-default: %v", err)
	}
	if chatA != "chat-a" || chatB != "chat-b" {
		t.Fatalf("knowledge bases must keep distinct bindings, got %q and %q", chatA, chatB)
	}
	if fallback != "chat-default" {
		t.Fatalf("unbound knowledge base must use deployment default, got %q", fallback)
	}

	// The tenant filter is part of the lookup, so a foreign tenant cannot route
	// through another tenant's knowledge base id.
	if _, err := registry.ChatModel(context.Background(), "tenant-2", "kb-a"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("expected not found across tenants, got %v", err)
	}
}

func TestRerankModelReadsTheKnowledgeBaseBinding(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-rerank", "tenant-1", "")
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb-rerank").
		Update("rerank_model", "rerank-a").Error; err != nil {
		t.Fatalf("set rerank binding: %v", err)
	}
	registry := newTestRegistry(t, db, Options{
		Models: []Model{
			{Name: "rerank-default", Provider: "remote", Capabilities: []model.Capability{model.CapabilityRerank}},
			{Name: "rerank-a", Provider: "remote", Capabilities: []model.Capability{model.CapabilityRerank}},
		},
		Defaults: map[model.Capability]string{model.CapabilityRerank: "rerank-default"},
	})

	resolved, err := registry.RerankModel(context.Background(), "tenant-1", "kb-rerank")
	if err != nil {
		t.Fatalf("resolve rerank model: %v", err)
	}
	if resolved != "rerank-a" {
		t.Fatalf("retrieval must rerank with the knowledge base binding, got %q", resolved)
	}
}

func TestUnregisteredBindingDegradesToDefault(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-stale", "tenant-1", "chat-retired")
	seedKnowledgeBase(t, db, "kb-empty", "tenant-1", "")
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(nil),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})

	resolution, err := registry.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-stale", model.CapabilityChat)
	if err != nil {
		t.Fatalf("resolve stale binding: %v", err)
	}
	if !resolution.Degraded || resolution.Effective.Model != "chat-default" || resolution.Requested.Model != "chat-retired" {
		t.Fatalf("stale binding must degrade to default, got %+v", resolution)
	}
	if resolution.Reason != "bound model is not registered" {
		t.Fatalf("unexpected degradation reason: %q", resolution.Reason)
	}

	// With no registered default there is nothing to fall back to.
	noDefault := newTestRegistry(t, db, Options{Models: catalogModels(nil)})
	if _, err := noDefault.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-stale", model.CapabilityChat); err == nil || !strings.Contains(err.Error(), "no chat model is registered") {
		t.Fatalf("expected missing default error, got %v", err)
	}
}

func TestUnhealthyProviderDegradesToDefault(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-a", "tenant-1", "chat-a")
	probes := map[string]func(context.Context) error{
		"chat-a": func(context.Context) error { return errors.New("provider timeout") },
	}
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(probes),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})

	resolution, err := registry.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-a", model.CapabilityChat)
	if err != nil {
		t.Fatalf("resolve unhealthy binding: %v", err)
	}
	if !resolution.Degraded || resolution.Effective.Model != "chat-default" || resolution.Reason != "bound provider is unhealthy" {
		t.Fatalf("unhealthy provider must degrade to default, got %+v", resolution)
	}

	statuses := registry.Health(context.Background())
	if len(statuses) != 4 {
		t.Fatalf("expected four health entries, got %d", len(statuses))
	}
	if statuses[0].Model != "chat-a" || statuses[0].Healthy || statuses[0].Error != "provider timeout" {
		t.Fatalf("unexpected health status: %+v", statuses[0])
	}
}

func TestHealthCheckErrorsWhenNoHealthyProviderRemains(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-a", "tenant-1", "chat-a")
	failing := func(context.Context) error { return errors.New("down") }
	registry := newTestRegistry(t, db, Options{
		Models: catalogModels(map[string]func(context.Context) error{
			"chat-a": failing, "chat-default": failing,
		}),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})
	if _, err := registry.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-a", model.CapabilityChat); err == nil || !strings.Contains(err.Error(), "no healthy chat provider") {
		t.Fatalf("expected no healthy provider error, got %v", err)
	}

	// Degrading to the same model as the bound one would be a silent lie.
	same := newTestRegistry(t, db, Options{
		Models:   catalogModels(map[string]func(context.Context) error{"chat-default": failing}),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})
	seedKnowledgeBase(t, db, "kb-default", "tenant-1", "")
	if _, err := same.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-default", model.CapabilityChat); err == nil || !strings.Contains(err.Error(), "is unhealthy") {
		t.Fatalf("expected bound-model unhealthy error, got %v", err)
	}
}

type timeoutProbeError struct{}

func (timeoutProbeError) Error() string   { return "probe: context deadline exceeded" }
func (timeoutProbeError) Timeout() bool   { return true }
func (timeoutProbeError) Temporary() bool { return true }

func TestTransientProbeTimeoutDoesNotRejectSoleProvider(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-default", "tenant-1", "")
	registry := newTestRegistry(t, db, Options{
		Models: catalogModels(map[string]func(context.Context) error{
			"chat-default": func(context.Context) error { return timeoutProbeError{} },
		}),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})

	resolution, err := registry.ResolveForKnowledgeBase(context.Background(), "tenant-1", "kb-default", model.CapabilityChat)
	if err != nil {
		t.Fatalf("a timed-out probe must not reject the only chat provider: %v", err)
	}
	if !resolution.Degraded || resolution.Effective.Model != "chat-default" {
		t.Fatalf("expected the bound model to be attempted, got %+v", resolution)
	}
}

func TestHealthProbeIsCachedForTTL(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-a", "tenant-1", "chat-a")
	calls := 0
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	registry := newTestRegistry(t, db, Options{
		Models: catalogModels(map[string]func(context.Context) error{
			"chat-a": func(context.Context) error { calls++; return nil },
		}),
		Defaults:  map[model.Capability]string{model.CapabilityChat: "chat-default"},
		HealthTTL: 30 * time.Second,
		Now:       func() time.Time { return now },
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := registry.ChatModel(ctx, "tenant-1", "kb-a"); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected probe to be cached within TTL, got %d calls", calls)
	}
	now = now.Add(31 * time.Second)
	if _, err := registry.ChatModel(ctx, "tenant-1", "kb-a"); err != nil {
		t.Fatalf("resolve after TTL: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected probe to run after TTL, got %d calls", calls)
	}
}

func TestQuotaIsPerTenantAndCapability(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-a", "tenant-1", "chat-a")
	seedKnowledgeBase(t, db, "kb-b", "tenant-2", "chat-a")
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	registry := newTestRegistry(t, db, Options{
		Models:            catalogModels(nil),
		Defaults:          map[model.Capability]string{model.CapabilityChat: "chat-default"},
		RequestsPerMinute: 2,
		Now:               func() time.Time { return now },
	})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := registry.ChatModel(ctx, "tenant-1", "kb-a"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := registry.ChatModel(ctx, "tenant-1", "kb-a"); !errors.Is(err, apperror.ErrQuotaExceeded) {
		t.Fatalf("expected quota exceeded, got %v", err)
	}

	// Another tenant keeps its own budget.
	if _, err := registry.ChatModel(ctx, "tenant-2", "kb-b"); err != nil {
		t.Fatalf("tenant-2 must not share tenant-1 budget: %v", err)
	}

	now = now.Add(time.Minute)
	if _, err := registry.ChatModel(ctx, "tenant-1", "kb-a"); err != nil {
		t.Fatalf("quota must reset after a minute: %v", err)
	}
}

func TestZeroRequestBudgetDisablesQuota(t *testing.T) {
	db := openRegistryTestDB(t)
	seedKnowledgeBase(t, db, "kb-a", "tenant-1", "chat-a")
	registry := newTestRegistry(t, db, Options{
		Models:   catalogModels(nil),
		Defaults: map[model.Capability]string{model.CapabilityChat: "chat-default"},
	})
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if _, err := registry.ChatModel(ctx, "tenant-1", "kb-a"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
}
