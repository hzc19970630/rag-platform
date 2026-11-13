package modelregistry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// ProviderKinds are the wire dialects the first B13 cut can execute. Azure's
// deployment-scoped path and api-version are deliberately absent until that
// adapter exists, so a stored provider can always actually serve requests.
const (
	ProviderKindOpenAICompatible = "openai_compatible"
	ProviderKindOllama           = "ollama"
)

var providerKinds = map[string]struct{}{
	ProviderKindOpenAICompatible: {},
	ProviderKindOllama:           {},
}

// SecretCipher protects stored provider API keys. It is the same keyring the
// connector credentials use; the registry never sees the key material.
type SecretCipher interface {
	Encrypt(plaintext []byte) (string, error)
	Decrypt(envelope string) ([]byte, error)
}

// Actor identifies who changed the catalog. Audit rows are written by the
// registry so HTTP and future admin surfaces share one trail.
type Actor struct {
	ID        string
	RequestID string
	IPAddress string
	UserAgent string
}

// Provider is the tenant-visible view of a provider. The API key is never part
// of it; APIKeySet only reports whether one is stored.
type Provider struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	BaseURL   string         `json:"baseUrl"`
	APIKeySet bool           `json:"apiKeySet"`
	HealthURL string         `json:"healthUrl"`
	Enabled   bool           `json:"enabled"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Models    []CatalogEntry `json:"models"`
}

// CatalogEntry is one routable model. Capability and dimension are validated
// against what the deployment can actually do.
type CatalogEntry struct {
	ID          string           `json:"id"`
	Model       string           `json:"model"`
	DisplayName string           `json:"displayName"`
	Capability  model.Capability `json:"capability"`
	Version     string           `json:"version"`
	Dimension   int              `json:"dimension"`
	Enabled     bool             `json:"enabled"`
}

// ProviderRoute is the resolved connection for one model. APIKey is decrypted
// only for the composition root, which builds the adapter per provider.
type ProviderRoute struct {
	ProviderID string
	Kind       string
	BaseURL    string
	APIKey     string
	HealthURL  string
	Model      string
	Version    string
	Dimension  int
	Capability model.Capability
	UpdatedAt  time.Time
}

// ProviderInput is the whole-provider write shape. Models replace the entry
// list; a nil APIKey keeps the stored credential, an empty string clears it.
type ProviderInput struct {
	Name      string
	Kind      string
	BaseURL   string
	APIKey    *string
	HealthURL string
	Enabled   *bool
	Models    []CatalogEntryInput
}

type CatalogEntryInput struct {
	Model       string
	DisplayName string
	Capability  string
	Version     string
	Dimension   int
	Enabled     *bool
}

type ProviderTestResult struct {
	Healthy   bool      `json:"healthy"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

func newProviderID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}

// CatalogModel is one routable model in the merged deployment + tenant view.
// Source distinguishes a deployment adapter from an administrator entry without
// exposing credentials.
type CatalogModel struct {
	ID          string           `json:"id"`
	ProviderID  string           `json:"providerId"`
	Provider    string           `json:"provider"`
	Model       string           `json:"model"`
	DisplayName string           `json:"displayName"`
	Capability  model.Capability `json:"capability"`
	Version     string           `json:"version"`
	Dimension   int              `json:"dimension"`
	Source      string           `json:"source"`
	Enabled     bool             `json:"enabled"`
}

// ListCatalog merges the deployment catalog with the tenant providers so a
// knowledge-base settings form can offer both in one list.
func (r *Registry) ListCatalog(ctx context.Context, tenantID string) ([]CatalogModel, error) {
	r.mu.Lock()
	deploymentModels := make([]Model, 0, len(r.models))
	for _, entry := range r.models {
		deploymentModels = append(deploymentModels, entry)
	}
	r.mu.Unlock()
	sort.Slice(deploymentModels, func(i, j int) bool { return deploymentModels[i].Name < deploymentModels[j].Name })
	result := make([]CatalogModel, 0, len(deploymentModels))
	for _, entry := range deploymentModels {
		for _, capability := range entry.Capabilities {
			result = append(result, CatalogModel{
				Provider: entry.Provider, Model: entry.Name, DisplayName: entry.Name,
				Capability: capability, Version: entry.Version, Source: "deployment", Enabled: true,
			})
		}
	}
	var entries []models.ModelCatalogEntry
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Order("model ASC").Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("list model catalog: %w", err)
	}
	providerNames := make(map[string]string, 4)
	for _, entry := range entries {
		name, ok := providerNames[entry.ProviderID]
		if !ok {
			var provider models.ModelProvider
			if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("id = ?", entry.ProviderID).Take(&provider).Error; err != nil {
				continue
			}
			name = provider.Name
			providerNames[entry.ProviderID] = name
		}
		result = append(result, CatalogModel{
			ID: entry.ID, ProviderID: entry.ProviderID, Provider: name, Model: entry.Model,
			DisplayName: entry.DisplayName, Capability: model.Capability(entry.Capability),
			Version: entry.Version, Dimension: entry.Dimension, Source: "provider", Enabled: entry.Enabled,
		})
	}
	return result, nil
}

// ListProviders returns every provider of one tenant with its models. The
// entries are nested so the admin surface needs one request.
func (r *Registry) ListProviders(ctx context.Context, tenantID string) ([]Provider, error) {
	var rows []models.ModelProvider
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list model providers: %w", err)
	}
	var entries []models.ModelCatalogEntry
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Order("model ASC").Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("list model catalog entries: %w", err)
	}
	byProvider := make(map[string][]CatalogEntry, len(rows))
	for _, entry := range entries {
		byProvider[entry.ProviderID] = append(byProvider[entry.ProviderID], catalogEntryView(entry))
	}
	result := make([]Provider, 0, len(rows))
	for _, row := range rows {
		result = append(result, providerView(row, byProvider[row.ID]))
	}
	return result, nil
}

// CreateProvider stores a provider and its models. Nothing is applied to the
// request path until a knowledge base binds one of the models.
func (r *Registry) CreateProvider(ctx context.Context, tenantID string, actor Actor, input ProviderInput) (Provider, error) {
	normalized, enabled, keyCipher, entries, err := r.normalizeProviderInput(ctx, tenantID, input, nil)
	if err != nil {
		return Provider{}, err
	}
	id, err := newProviderID("mp")
	if err != nil {
		return Provider{}, err
	}
	row := models.ModelProvider{
		ID: id, TenantID: tenantID, Name: normalized.Name, Kind: normalized.Kind,
		BaseURL: normalized.BaseURL, HealthURL: normalized.HealthURL, Enabled: enabled,
		CreatedAt: r.now().UTC(), UpdatedAt: r.now().UTC(),
	}
	if keyCipher != nil {
		row.APIKeyCipher = *keyCipher
	}
	err = tenantdb.InTx(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		for index := range entries {
			entries[index].ID, err = newProviderID("mm")
			if err != nil {
				return err
			}
			entries[index].TenantID = tenantID
			entries[index].ProviderID = id
			entries[index].CreatedAt = row.CreatedAt
			entries[index].UpdatedAt = row.UpdatedAt
			if err := tx.Create(&entries[index]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Provider{}, fmt.Errorf("create model provider: %w", err)
	}
	if err := r.recordProviderAudit(ctx, tenantID, actor, "model_provider_created", id, fmt.Sprintf("name=%s kind=%s models=%d", row.Name, row.Kind, len(entries))); err != nil {
		return Provider{}, err
	}
	return providerView(row, catalogEntryViews(entries)), nil
}

// UpdateProvider replaces the provider fields and its model list. Entries the
// knowledge bases still bind cannot be removed or re-typed.
func (r *Registry) UpdateProvider(ctx context.Context, tenantID string, actor Actor, id string, input ProviderInput) (Provider, error) {
	var current models.ModelProvider
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("id = ?", id).Take(&current).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return Provider{}, apperror.ErrNotFound
		}
		return Provider{}, fmt.Errorf("load model provider: %w", err)
	}
	normalized, enabled, keyCipher, entries, err := r.normalizeProviderInput(ctx, tenantID, input, &current)
	if err != nil {
		return Provider{}, err
	}
	var existing []models.ModelCatalogEntry
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("provider_id = ?", id).Find(&existing).Error; err != nil {
		return Provider{}, fmt.Errorf("load catalog entries: %w", err)
	}
	kept := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		kept[entry.Model] = struct{}{}
	}
	for _, old := range existing {
		if _, ok := kept[old.Model]; !ok {
			if bound, err := r.modelIsBound(ctx, tenantID, old.Model); err != nil {
				return Provider{}, err
			} else if bound {
				return Provider{}, fmt.Errorf("%w: model %q is bound to a knowledge base", apperror.ErrConflict, old.Model)
			}
			continue
		}
		// A bound entry keeps its meaning: capability and the embedding
		// dimension define what the stored vectors and prompts mean.
		for _, entry := range entries {
			if entry.Model != old.Model {
				continue
			}
			if entry.Capability == old.Capability && entry.Dimension == old.Dimension {
				continue
			}
			if bound, err := r.modelIsBound(ctx, tenantID, old.Model); err != nil {
				return Provider{}, err
			} else if bound {
				return Provider{}, fmt.Errorf("%w: model %q is bound to a knowledge base; only its version may change", apperror.ErrConflict, old.Model)
			}
		}
	}
	now := r.now().UTC()
	current.Name = normalized.Name
	current.Kind = normalized.Kind
	current.BaseURL = normalized.BaseURL
	current.HealthURL = normalized.HealthURL
	current.Enabled = enabled
	current.UpdatedAt = now
	if keyCipher != nil {
		current.APIKeyCipher = *keyCipher
	}
	err = tenantdb.InTx(ctx, r.db, tenantID, func(tx *gorm.DB) error {
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		byModel := make(map[string]models.ModelCatalogEntry, len(existing))
		for _, row := range existing {
			byModel[row.Model] = row
		}
		for _, old := range existing {
			if _, ok := kept[old.Model]; !ok {
				if err := tenantdb.MustScoped(tx, tenantID).Where("id = ?", old.ID).Delete(&models.ModelCatalogEntry{}).Error; err != nil {
					return err
				}
			}
		}
		for index := range entries {
			entry := &entries[index]
			entry.TenantID = tenantID
			entry.ProviderID = id
			entry.UpdatedAt = now
			if previous, ok := byModel[entry.Model]; ok {
				entry.ID = previous.ID
				entry.CreatedAt = previous.CreatedAt
				if err := tx.Save(entry).Error; err != nil {
					return err
				}
				continue
			}
			entry.ID, err = newProviderID("mm")
			if err != nil {
				return err
			}
			entry.CreatedAt = now
			if err := tx.Create(entry).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Provider{}, fmt.Errorf("update model provider: %w", err)
	}
	if err := r.recordProviderAudit(ctx, tenantID, actor, "model_provider_updated", id, fmt.Sprintf("name=%s kind=%s models=%d", current.Name, current.Kind, len(entries))); err != nil {
		return Provider{}, err
	}
	return providerView(current, catalogEntryViews(entries)), nil
}

// DeleteProvider removes a provider once no knowledge base binds its models.
func (r *Registry) DeleteProvider(ctx context.Context, tenantID string, actor Actor, id string) error {
	var row models.ModelProvider
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("id = ?", id).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return apperror.ErrNotFound
		}
		return fmt.Errorf("load model provider: %w", err)
	}
	var entries []models.ModelCatalogEntry
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("provider_id = ?", id).Find(&entries).Error; err != nil {
		return fmt.Errorf("load catalog entries: %w", err)
	}
	for _, entry := range entries {
		bound, err := r.modelIsBound(ctx, tenantID, entry.Model)
		if err != nil {
			return err
		}
		if bound {
			return fmt.Errorf("%w: model %q is bound to a knowledge base", apperror.ErrConflict, entry.Model)
		}
	}
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("id = ?", id).Delete(&models.ModelProvider{}).Error; err != nil {
		return fmt.Errorf("delete model provider: %w", err)
	}
	return r.recordProviderAudit(ctx, tenantID, actor, "model_provider_deleted", id, "name="+row.Name)
}

// TestProvider probes the provider without persisting health, so an admin can
// try a candidate endpoint before saving it.
func (r *Registry) TestProvider(ctx context.Context, tenantID string, actor Actor, id string) (ProviderTestResult, error) {
	var row models.ModelProvider
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("id = ?", id).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return ProviderTestResult{}, apperror.ErrNotFound
		}
		return ProviderTestResult{}, fmt.Errorf("load model provider: %w", err)
	}
	result := ProviderTestResult{Healthy: true, CheckedAt: r.now().UTC()}
	var entries []models.ModelCatalogEntry
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("provider_id = ?", id).Order("model ASC").Find(&entries).Error; err != nil {
		return ProviderTestResult{}, fmt.Errorf("load catalog entries: %w", err)
	}
	if err := r.probeProviderRow(ctx, row, entries); err != nil {
		result.Healthy = false
		result.Error = err.Error()
	}
	if err := r.recordProviderAudit(ctx, tenantID, actor, "model_provider_tested", id, fmt.Sprintf("healthy=%t", result.Healthy)); err != nil {
		return ProviderTestResult{}, err
	}
	return result, nil
}

// ProviderForModel resolves one catalog entry to a routed connection. The
// composition root calls it per request and caches adapters by UpdatedAt.
func (r *Registry) ProviderForModel(ctx context.Context, tenantID, modelName string) (ProviderRoute, error) {
	name := strings.TrimSpace(modelName)
	if name == "" {
		return ProviderRoute{}, apperror.ErrNotFound
	}
	var entry models.ModelCatalogEntry
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("model = ?", name).Take(&entry).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return ProviderRoute{}, apperror.ErrNotFound
		}
		return ProviderRoute{}, fmt.Errorf("load catalog entry: %w", err)
	}
	if !entry.Enabled {
		return ProviderRoute{}, fmt.Errorf("%w: model %q is disabled", apperror.ErrNotFound, entry.Model)
	}
	var provider models.ModelProvider
	if err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("id = ?", entry.ProviderID).Take(&provider).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return ProviderRoute{}, apperror.ErrNotFound
		}
		return ProviderRoute{}, fmt.Errorf("load model provider: %w", err)
	}
	if !provider.Enabled {
		return ProviderRoute{}, fmt.Errorf("%w: provider %q is disabled", apperror.ErrNotFound, provider.Name)
	}
	return r.routeFor(provider, entry)
}

func (r *Registry) normalizeProviderInput(ctx context.Context, tenantID string, input ProviderInput, current *models.ModelProvider) (ProviderInput, bool, *string, []models.ModelCatalogEntry, error) {
	normalized := input
	normalized.Name = strings.TrimSpace(input.Name)
	if normalized.Name == "" || len(normalized.Name) > 128 {
		return ProviderInput{}, false, nil, nil, apperror.Invalidf("provider name must be 1-128 characters")
	}
	normalized.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	if _, ok := providerKinds[normalized.Kind]; !ok {
		return ProviderInput{}, false, nil, nil, apperror.Invalidf("unsupported provider kind %q", input.Kind)
	}
	baseURL, err := normalizeEndpoint(input.BaseURL)
	if err != nil {
		return ProviderInput{}, false, nil, nil, apperror.Invalidf("invalid base url: %v", err)
	}
	normalized.BaseURL = baseURL
	if strings.TrimSpace(input.HealthURL) == "" {
		normalized.HealthURL = ""
	} else {
		healthURL, err := normalizeEndpoint(input.HealthURL)
		if err != nil {
			return ProviderInput{}, false, nil, nil, apperror.Invalidf("invalid health url: %v", err)
		}
		normalized.HealthURL = healthURL
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	var keyCipher *string
	if input.APIKey != nil {
		plaintext := strings.TrimSpace(*input.APIKey)
		sealed := ""
		if plaintext != "" {
			if r.cipher == nil {
				return ProviderInput{}, false, nil, nil, apperror.Invalidf("credential encryption is not configured")
			}
			sealed, err = r.cipher.Encrypt([]byte(plaintext))
			if err != nil {
				return ProviderInput{}, false, nil, nil, fmt.Errorf("encrypt provider credential: %w", err)
			}
		}
		keyCipher = &sealed
	}
	if err := r.validateProviderName(ctx, tenantID, normalized.Name, current); err != nil {
		return ProviderInput{}, false, nil, nil, err
	}
	providerID := ""
	if current != nil {
		providerID = current.ID
	}
	entries, err := r.buildEntries(ctx, tenantID, providerID, input.Models)
	if err != nil {
		return ProviderInput{}, false, nil, nil, err
	}
	normalized.Models = nil
	return normalized, enabled, keyCipher, entries, nil
}

func (r *Registry) validateProviderName(ctx context.Context, tenantID, name string, current *models.ModelProvider) error {
	var count int64
	query := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Model(&models.ModelProvider{}).Where("name = ?", name)
	if current != nil {
		query = query.Where("id <> ?", current.ID)
	}
	if err := query.Count(&count).Error; err != nil {
		return fmt.Errorf("check provider name: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: provider name %q already exists", apperror.ErrConflict, name)
	}
	return nil
}

func (r *Registry) buildEntries(ctx context.Context, tenantID, providerID string, inputs []CatalogEntryInput) ([]models.ModelCatalogEntry, error) {
	entries := make([]models.ModelCatalogEntry, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		name := strings.TrimSpace(input.Model)
		if name == "" || len(name) > 128 {
			return nil, apperror.Invalidf("model name must be 1-128 characters")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, apperror.Invalidf("model %q is listed twice", name)
		}
		seen[name] = struct{}{}
		if _, reserved := r.models[name]; reserved {
			return nil, fmt.Errorf("%w: model %q is already served by the deployment catalog", apperror.ErrConflict, name)
		}
		capability := model.Capability(strings.ToLower(strings.TrimSpace(input.Capability)))
		switch capability {
		case model.CapabilityChat, model.CapabilityEmbedding, model.CapabilityRerank:
		default:
			return nil, apperror.Invalidf("unsupported model capability %q", input.Capability)
		}
		dimension := input.Dimension
		if capability == model.CapabilityEmbedding {
			if dimension <= 0 {
				return nil, apperror.Invalidf("embedding model %q requires a positive dimension", name)
			}
		} else {
			dimension = 0
		}
		version := strings.TrimSpace(input.Version)
		if version == "" {
			version = "deployment"
		}
		displayName := strings.TrimSpace(input.DisplayName)
		if displayName == "" {
			displayName = name
		}
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		var existing models.ModelCatalogEntry
		err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Where("model = ?", name).Take(&existing).Error
		if err == nil && existing.ProviderID != providerID {
			return nil, fmt.Errorf("%w: model %q is already registered", apperror.ErrConflict, name)
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("check model name: %w", err)
		}
		entries = append(entries, models.ModelCatalogEntry{
			Model: name, DisplayName: displayName, Capability: string(capability),
			Version: version, Dimension: dimension, Enabled: enabled,
		})
	}
	return entries, nil
}

// modelIsBound reports whether any knowledge base in the tenant still routes
// through the model. Removing a bound entry would silently break a binding.
func (r *Registry) modelIsBound(ctx context.Context, tenantID, modelName string) (bool, error) {
	var count int64
	err := tenantdb.MustScoped(r.db.WithContext(ctx), tenantID).Model(&models.KnowledgeBase{}).
		Where("chat_model = ? OR rerank_model = ? OR embedding_model = ?", modelName, modelName, modelName).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check model binding: %w", err)
	}
	return count > 0, nil
}

func providerView(row models.ModelProvider, entries []CatalogEntry) Provider {
	return Provider{
		ID: row.ID, Name: row.Name, Kind: row.Kind, BaseURL: row.BaseURL,
		APIKeySet: strings.TrimSpace(row.APIKeyCipher) != "", HealthURL: row.HealthURL,
		Enabled: row.Enabled, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Models: entries,
	}
}

func catalogEntryView(row models.ModelCatalogEntry) CatalogEntry {
	return CatalogEntry{
		ID: row.ID, Model: row.Model, DisplayName: row.DisplayName,
		Capability: model.Capability(row.Capability), Version: row.Version,
		Dimension: row.Dimension, Enabled: row.Enabled,
	}
}

func catalogEntryViews(rows []models.ModelCatalogEntry) []CatalogEntry {
	views := make([]CatalogEntry, 0, len(rows))
	for _, row := range rows {
		views = append(views, catalogEntryView(row))
	}
	return views
}

func normalizeEndpoint(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("endpoint is required")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("host is required")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("credentials must not be embedded in the url")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("query and fragment are not supported")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (r *Registry) recordProviderAudit(ctx context.Context, tenantID string, actor Actor, action, resourceID, reason string) error {
	if r.db == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: tenantID, ActorID: actor.ID, Action: action,
		ResourceType: "model_provider", ResourceID: resourceID, Allowed: true,
		Reason: reason, RequestID: actor.RequestID, IPAddress: actor.IPAddress, UserAgent: actor.UserAgent,
	}).Error
}
