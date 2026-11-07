// Package modelregistry owns the deployment model catalog and the
// tenant/knowledge-base bindings that route chat, embedding and rerank calls.
// It is deliberately in-process: the catalog describes the adapters this
// deployment actually built, so a binding can never name a model nobody serves.
package modelregistry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const defaultHealthTTL = 30 * time.Second

// Model is one adapter this deployment can route to. Probe reports provider
// health; nil means the adapter has no remote dependency and is always healthy.
type Model struct {
	Name         string
	Provider     string
	Version      string
	Capabilities []model.Capability
	Probe        func(context.Context) error
}

// Resolution is the effective model for one knowledge base after applying the
// configured binding, provider health and the fallback default.
type Resolution struct {
	Requested model.ModelRef
	Effective model.ModelRef
	Degraded  bool
	Reason    string
}

// HealthStatus is one provider probe result.
type HealthStatus struct {
	Model     string
	Provider  string
	Version   string
	Healthy   bool
	Error     string
	CheckedAt time.Time
}

// Options configures the deployment catalog.
type Options struct {
	Models []Model
	// Defaults maps each routed capability to the model used when a knowledge
	// base has no binding or its bound provider is degraded.
	Defaults map[model.Capability]string
	// RequestsPerMinute bounds catalog resolutions per tenant and capability.
	// Zero disables the limit.
	RequestsPerMinute int
	HealthTTL         time.Duration
	Now               func() time.Time
}

// Registry is safe for concurrent use by the API and worker processes.
type Registry struct {
	db                *gorm.DB
	requestsPerMinute int
	healthTTL         time.Duration
	now               func() time.Time

	mu       sync.Mutex
	models   map[string]Model
	defaults map[model.Capability]string
	health   map[string]healthEntry
	usage    map[string]usageWindow
}

type healthEntry struct {
	healthy  bool
	timedOut bool
	err      string
	checked  time.Time
}

type usageWindow struct {
	start time.Time
	count int
}

// New validates the catalog before it is wired into request handling.
func New(db *gorm.DB, options Options) (*Registry, error) {
	if options.HealthTTL <= 0 {
		options.HealthTTL = defaultHealthTTL
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	registry := &Registry{
		db:                db,
		requestsPerMinute: options.RequestsPerMinute,
		healthTTL:         options.HealthTTL,
		now:               options.Now,
		models:            make(map[string]Model, len(options.Models)),
		defaults:          make(map[model.Capability]string, len(options.Defaults)),
		health:            make(map[string]healthEntry),
		usage:             make(map[string]usageWindow),
	}
	for _, entry := range options.Models {
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" {
			return nil, fmt.Errorf("model registry: model name is required")
		}
		if len(entry.Capabilities) == 0 {
			return nil, fmt.Errorf("model registry: model %q has no capability", entry.Name)
		}
		if entry.Version == "" {
			entry.Version = "deployment"
		}
		if _, exists := registry.models[entry.Name]; exists {
			return nil, fmt.Errorf("model registry: duplicate model %q", entry.Name)
		}
		registry.models[entry.Name] = entry
	}
	for capability, name := range options.Defaults {
		entry, ok := registry.models[name]
		if !ok || !entry.supports(capability) {
			return nil, fmt.Errorf("model registry: default %s model %q is not registered", capability, name)
		}
		registry.defaults[capability] = name
	}
	return registry, nil
}

func (m Model) supports(capability model.Capability) bool {
	for _, candidate := range m.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func (m Model) ref() model.ModelRef {
	return model.ModelRef{Model: m.Name, Provider: m.Provider, Version: m.Version}
}

// ResolveBinding validates a binding write. An empty request resolves to the
// deployment default, which is how a knowledge base inherits routing.
func (r *Registry) ResolveBinding(capability model.Capability, requested string) (model.ModelRef, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = r.defaults[capability]
	}
	entry, ok := r.models[name]
	if !ok || !entry.supports(capability) {
		return model.ModelRef{}, apperror.Invalidf("unsupported %s model %q", capability, name)
	}
	return entry.ref(), nil
}

// ResolveForKnowledgeBase reads the knowledge base binding, verifies provider
// health and falls back to the deployment default when the bound provider is
// degraded. It also enforces the per-tenant model request budget.
func (r *Registry) ResolveForKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string, capability model.Capability) (Resolution, error) {
	var row models.KnowledgeBase
	if err := r.db.WithContext(ctx).
		Select("id", "tenant_id", "chat_model", "chat_model_version", "rerank_model", "rerank_model_version", "embedding_model", "embedding_dimension").
		Where("tenant_id = ? AND id = ?", tenantID, knowledgeBaseID).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return Resolution{}, apperror.ErrNotFound
		}
		return Resolution{}, fmt.Errorf("load model binding: %w", err)
	}
	requested := bindingName(row, capability)
	resolution, err := r.resolve(ctx, capability, requested)
	if err != nil {
		return Resolution{}, err
	}
	if err := r.consumeQuota(tenantID, capability); err != nil {
		return Resolution{}, err
	}
	return resolution, nil
}

func bindingName(row models.KnowledgeBase, capability model.Capability) string {
	switch capability {
	case model.CapabilityChat:
		return row.ChatModel
	case model.CapabilityRerank:
		return row.RerankModel
	case model.CapabilityEmbedding:
		return row.EmbeddingModel
	default:
		return ""
	}
}

func (r *Registry) resolve(ctx context.Context, capability model.Capability, requested string) (Resolution, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = r.defaults[capability]
	}
	bound, ok := r.models[name]
	if !ok || !bound.supports(capability) {
		// A binding can outlive a catalog entry after a deployment rollback.
		// Degrading to the default is safer than failing every query.
		name = r.defaults[capability]
		bound, ok = r.models[name]
		if !ok || !bound.supports(capability) {
			return Resolution{}, fmt.Errorf("model registry: no %s model is registered", capability)
		}
		return r.degraded(model.ModelRef{Model: requested}, bound, "bound model is not registered")
	}
	healthy, timedOut := r.probeHealth(ctx, bound)
	if healthy {
		return Resolution{Requested: bound.ref(), Effective: bound.ref()}, nil
	}
	fallbackName := r.defaults[capability]
	fallback, ok := r.models[fallbackName]
	if !ok || !fallback.supports(capability) || fallback.Name == bound.Name {
		if timedOut {
			// A probe timeout is inconclusive and must not become a request-path
			// outage: attempt the real call and let it report its own failure.
			return r.degraded(bound.ref(), bound, "provider health probe timed out; attempting the bound model")
		}
		return Resolution{}, fmt.Errorf("model registry: %s provider for model %q is unhealthy", capability, bound.Name)
	}
	fallbackHealthy, fallbackTimedOut := r.probeHealth(ctx, fallback)
	if !fallbackHealthy {
		if fallbackTimedOut {
			return r.degraded(bound.ref(), fallback, "fallback provider health probe timed out; attempting it")
		}
		if timedOut {
			return r.degraded(bound.ref(), bound, "fallback provider is unhealthy; attempting the bound model")
		}
		return Resolution{}, fmt.Errorf("model registry: no healthy %s provider is available", capability)
	}
	return r.degraded(bound.ref(), fallback, "bound provider is unhealthy")
}

func (r *Registry) degraded(requested model.ModelRef, effective Model, reason string) (Resolution, error) {
	return Resolution{Requested: requested, Effective: effective.ref(), Degraded: true, Reason: reason}, nil
}

// ChatModel is the conversation-facing resolver. An empty string means the
// adapter's configured model, which is what offline adapters expect.
func (r *Registry) ChatModel(ctx context.Context, tenantID, knowledgeBaseID string) (string, error) {
	resolution, err := r.ResolveForKnowledgeBase(ctx, tenantID, knowledgeBaseID, model.CapabilityChat)
	if err != nil {
		return "", err
	}
	return resolution.Effective.Model, nil
}

// RerankModel is the retrieval-facing resolver. It performs the same binding,
// health and quota checks as ChatModel.
func (r *Registry) RerankModel(ctx context.Context, tenantID, knowledgeBaseID string) (string, error) {
	resolution, err := r.ResolveForKnowledgeBase(ctx, tenantID, knowledgeBaseID, model.CapabilityRerank)
	if err != nil {
		return "", err
	}
	return resolution.Effective.Model, nil
}

// probeHealth returns the cached provider state and whether the last failure
// was a timeout. A timeout is treated as inconclusive: it may only be the
// health endpoint being slow while completions still work.
func (r *Registry) probeHealth(ctx context.Context, entry Model) (healthy, timedOut bool) {
	if entry.Probe == nil {
		return true, false
	}
	r.mu.Lock()
	cached, ok := r.health[entry.Name]
	r.mu.Unlock()
	now := r.now()
	if ok && now.Sub(cached.checked) < r.healthTTL {
		return cached.healthy, cached.timedOut
	}
	err := entry.Probe(ctx)
	next := healthEntry{healthy: err == nil, timedOut: probeTimedOut(err), checked: now}
	if err != nil {
		next.err = err.Error()
	}
	r.mu.Lock()
	r.health[entry.Name] = next
	r.mu.Unlock()
	return next.healthy, next.timedOut
}

func probeTimedOut(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (r *Registry) consumeQuota(tenantID string, capability model.Capability) error {
	if r.requestsPerMinute <= 0 {
		return nil
	}
	now := r.now()
	key := tenantID + "|" + string(capability)
	r.mu.Lock()
	defer r.mu.Unlock()
	window := r.usage[key]
	if window.start.IsZero() || now.Sub(window.start) >= time.Minute {
		window = usageWindow{start: now}
	}
	if window.count >= r.requestsPerMinute {
		return fmt.Errorf("%w: the model request budget of %d per minute has been consumed", apperror.ErrQuotaExceeded, r.requestsPerMinute)
	}
	window.count++
	r.usage[key] = window
	return nil
}

// Health probes every provider and returns the snapshot used for degradation.
func (r *Registry) Health(ctx context.Context) []HealthStatus {
	r.mu.Lock()
	entries := make([]Model, 0, len(r.models))
	for _, entry := range r.models {
		entries = append(entries, entry)
	}
	r.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	statuses := make([]HealthStatus, 0, len(entries))
	for _, entry := range entries {
		healthy, _ := r.probeHealth(ctx, entry)
		status := HealthStatus{Model: entry.Name, Provider: entry.Provider, Version: entry.Version, Healthy: healthy, CheckedAt: r.now()}
		r.mu.Lock()
		if cached, ok := r.health[entry.Name]; ok {
			status.Error = cached.err
		}
		r.mu.Unlock()
		statuses = append(statuses, status)
	}
	return statuses
}
