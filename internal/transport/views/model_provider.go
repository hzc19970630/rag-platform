package views

import (
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
)

// ModelProvider maps one stored provider to the admin wire shape. Credentials
// never cross this boundary; APIKeySet only says whether one is stored.
func ModelProvider(provider modelregistry.Provider) httpapi.ModelProvider {
	models := make([]httpapi.ModelCatalogEntry, 0, len(provider.Models))
	for _, entry := range provider.Models {
		models = append(models, httpapi.ModelCatalogEntry{
			ID: entry.ID, ProviderID: provider.ID, Provider: provider.Name,
			Model: entry.Model, DisplayName: entry.DisplayName, Capability: string(entry.Capability),
			Version: entry.Version, Dimension: entry.Dimension, Source: "provider", Enabled: entry.Enabled,
		})
	}
	return httpapi.ModelProvider{
		ID: provider.ID, Name: provider.Name, Kind: provider.Kind, BaseURL: provider.BaseURL,
		APIKeySet: provider.APIKeySet, HealthURL: provider.HealthURL, Enabled: provider.Enabled,
		CreatedAt: provider.CreatedAt, UpdatedAt: provider.UpdatedAt, Models: models,
	}
}

func ModelProviders(providers []modelregistry.Provider) []httpapi.ModelProvider {
	items := make([]httpapi.ModelProvider, 0, len(providers))
	for _, provider := range providers {
		items = append(items, ModelProvider(provider))
	}
	return items
}

// ModelCatalog merges deployment and provider models for settings forms.
func ModelCatalog(models []modelregistry.CatalogModel) httpapi.ModelCatalog {
	items := make([]httpapi.ModelCatalogEntry, 0, len(models))
	for _, entry := range models {
		items = append(items, httpapi.ModelCatalogEntry{
			ID: entry.ID, ProviderID: entry.ProviderID, Provider: entry.Provider,
			Model: entry.Model, DisplayName: entry.DisplayName, Capability: string(entry.Capability),
			Version: entry.Version, Dimension: entry.Dimension, Source: entry.Source, Enabled: entry.Enabled,
		})
	}
	return httpapi.ModelCatalog{Items: items}
}

// ModelProviderInput converts the whole-provider request into the domain shape.
func ModelProviderInput(input httpapi.ModelProviderUpsertRequest) modelregistry.ProviderInput {
	entries := make([]modelregistry.CatalogEntryInput, 0, len(input.Models))
	for _, entry := range input.Models {
		entries = append(entries, modelregistry.CatalogEntryInput{
			Model: entry.Model, DisplayName: entry.DisplayName, Capability: entry.Capability,
			Version: entry.Version, Dimension: entry.Dimension, Enabled: entry.Enabled,
		})
	}
	return modelregistry.ProviderInput{
		Name: input.Name, Kind: input.Kind, BaseURL: input.BaseURL, APIKey: input.APIKey,
		HealthURL: input.HealthURL, Enabled: input.Enabled, Models: entries,
	}
}

func ModelProviderTestResult(result modelregistry.ProviderTestResult) httpapi.ModelProviderTestResult {
	return httpapi.ModelProviderTestResult{Healthy: result.Healthy, Error: result.Error, CheckedAt: result.CheckedAt}
}
