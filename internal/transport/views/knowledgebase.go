package views

import (
	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
)

func KnowledgeBase(row models.KnowledgeBase) httpapi.KnowledgeBase {
	return httpapi.KnowledgeBase{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		OwnerID:            row.OwnerID,
		Name:               row.Name,
		Description:        row.Description,
		EmbeddingModel:     row.EmbeddingModel,
		EmbeddingDimension: row.EmbeddingDimension,
		ACLVersion:         row.ACLVersion,
		RetrievalConfig:    row.RetrievalConfig,
		IndexingStrategy:   row.IndexingStrategy,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func KnowledgeBases(rows []models.KnowledgeBase) []httpapi.KnowledgeBase {
	items := make([]httpapi.KnowledgeBase, 0, len(rows))
	for _, row := range rows {
		items = append(items, KnowledgeBase(row))
	}
	return items
}

func KnowledgeBaseMember(row models.KnowledgeBaseMember) httpapi.KnowledgeBaseMember {
	return httpapi.KnowledgeBaseMember{
		KnowledgeBaseID: row.KnowledgeBaseID,
		UserID:          row.UserID,
		Role:            row.Role,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func KnowledgeBaseMembers(rows []models.KnowledgeBaseMember) []httpapi.KnowledgeBaseMember {
	items := make([]httpapi.KnowledgeBaseMember, 0, len(rows))
	for _, row := range rows {
		items = append(items, KnowledgeBaseMember(row))
	}
	return items
}

func CreateKnowledgeBaseInput(input httpapi.KnowledgeBaseCreateRequest) knowledgebase.CreateInput {
	return knowledgebase.CreateInput{Name: input.Name, Description: input.Description}
}

func UpdateKnowledgeBaseInput(input httpapi.KnowledgeBaseUpdateRequest) knowledgebase.UpdateInput {
	result := knowledgebase.UpdateInput{
		Name:               input.Name,
		Description:        input.Description,
		EmbeddingModel:     input.EmbeddingModel,
		EmbeddingDimension: input.EmbeddingDimension,
		ChatModel:          input.ChatModel,
		RerankModel:        input.RerankModel,
	}
	if input.RetrievalConfig != nil {
		override := ParamsOverride(*input.RetrievalConfig)
		result.RetrievalConfig = &override
	}
	if input.IndexingStrategy != nil {
		strategy := IndexingStrategyInput(*input.IndexingStrategy)
		result.IndexingStrategy = &strategy
	}
	return result
}

func AddKnowledgeBaseMemberInput(input httpapi.KnowledgeBaseMemberCreateRequest) knowledgebase.AddMemberInput {
	return knowledgebase.AddMemberInput{UserID: input.UserID, Role: input.Role}
}

func KnowledgeBaseConfiguration(view knowledgebase.KnowledgeBaseConfigurationV1) httpapi.KnowledgeBaseConfiguration {
	return httpapi.KnowledgeBaseConfiguration{
		SchemaVersion:    view.SchemaVersion,
		KnowledgeBaseID:  view.KnowledgeBaseID,
		ACLVersion:       view.ACLVersion,
		IndexingStrategy: IndexingStrategy(view.IndexingStrategy),
		Retrieval:        RetrievalConfiguration(view.Retrieval),
		ReadOnly:         ReadOnlySystemConfiguration(view.ReadOnly),
	}
}

func RetrievalConfiguration(view knowledgebase.RetrievalConfigurationV1) httpapi.RetrievalConfiguration {
	return httpapi.RetrievalConfiguration{
		GlobalDefaults:        RetrievalParams(view.GlobalDefaults),
		KnowledgeBaseOverride: RetrievalOverride(view.KnowledgeBaseOverride),
		Effective:             RetrievalParams(view.Effective),
		Sources:               cloneMap(view.Sources),
	}
}

func RetrievalParams(view knowledgebase.RetrievalParamsV1) httpapi.RetrievalParams {
	return httpapi.RetrievalParams{
		TopK:                 view.TopK,
		EmbeddingTopK:        view.EmbeddingTopK,
		KeywordTopK:          view.KeywordTopK,
		VectorThreshold:      view.VectorThreshold,
		KeywordThreshold:     view.KeywordThreshold,
		RerankEnabled:        view.RerankEnabled,
		RerankTopK:           view.RerankTopK,
		RerankThreshold:      view.RerankThreshold,
		VectorEFSearch:       view.VectorEFSearch,
		VectorOversample:     view.VectorOversample,
		VectorIterativeScan:  view.VectorIterativeScan,
		ContextTokenLimit:    view.ContextTokenLimit,
		AdjacentExpansion:    view.AdjacentExpansion,
		MaxAdjacentChunks:    view.MaxAdjacentChunks,
		RRFK:                 view.RRFK,
		ExpansionScoreFactor: view.ExpansionScoreFactor,
	}
}

func IndexingStrategy(view retrieval.IndexingStrategy) httpapi.IndexingStrategy {
	return httpapi.IndexingStrategy{
		VectorEnabled:        view.VectorEnabled,
		KeywordEnabled:       view.KeywordEnabled,
		WikiEnabled:          view.WikiEnabled,
		GraphEnabled:         view.GraphEnabled,
		RequiredCapabilities: cloneStrings(view.RequiredCapabilities),
	}
}

func IndexingStrategyInput(input httpapi.IndexingStrategy) retrieval.IndexingStrategy {
	return retrieval.IndexingStrategy{
		VectorEnabled:        input.VectorEnabled,
		KeywordEnabled:       input.KeywordEnabled,
		WikiEnabled:          input.WikiEnabled,
		GraphEnabled:         input.GraphEnabled,
		RequiredCapabilities: cloneStrings(input.RequiredCapabilities),
	}
}

func RetrievalOverride(view retrieval.ParamsOverride) httpapi.RetrievalConfigOverride {
	return httpapi.RetrievalConfigOverride{
		TopK:                 view.TopK,
		EmbeddingTopK:        view.EmbeddingTopK,
		KeywordTopK:          view.KeywordTopK,
		VectorThreshold:      view.VectorThreshold,
		KeywordThreshold:     view.KeywordThreshold,
		RerankEnabled:        view.RerankEnabled,
		RerankTopK:           view.RerankTopK,
		RerankThreshold:      view.RerankThreshold,
		VectorEFSearch:       view.VectorEFSearch,
		VectorOversample:     view.VectorOversample,
		VectorIterativeScan:  view.VectorIterativeScan,
		ContextTokenLimit:    view.ContextTokenLimit,
		AdjacentExpansion:    view.AdjacentExpansion,
		MaxAdjacentChunks:    view.MaxAdjacentChunks,
		RRFK:                 view.RRFK,
		ExpansionScoreFactor: view.ExpansionScoreFactor,
	}
}

func ParamsOverride(view httpapi.RetrievalConfigOverride) retrieval.ParamsOverride {
	return retrieval.ParamsOverride{
		TopK:                 view.TopK,
		EmbeddingTopK:        view.EmbeddingTopK,
		KeywordTopK:          view.KeywordTopK,
		VectorThreshold:      view.VectorThreshold,
		KeywordThreshold:     view.KeywordThreshold,
		RerankEnabled:        view.RerankEnabled,
		RerankTopK:           view.RerankTopK,
		RerankThreshold:      view.RerankThreshold,
		VectorEFSearch:       view.VectorEFSearch,
		VectorOversample:     view.VectorOversample,
		VectorIterativeScan:  view.VectorIterativeScan,
		ContextTokenLimit:    view.ContextTokenLimit,
		AdjacentExpansion:    view.AdjacentExpansion,
		MaxAdjacentChunks:    view.MaxAdjacentChunks,
		RRFK:                 view.RRFK,
		ExpansionScoreFactor: view.ExpansionScoreFactor,
	}
}

func ReadOnlySystemConfiguration(view knowledgebase.ReadOnlySystemConfigurationV1) httpapi.ReadOnlySystemConfiguration {
	return httpapi.ReadOnlySystemConfiguration{
		Embedding: httpapi.ReadOnlyEmbedding{
			Source: view.Embedding.Source, Model: view.Embedding.Model, Dimension: view.Embedding.Dimension,
		},
		Chunking: httpapi.ReadOnlyChunking{
			Size: view.Chunking.Size, Overlap: view.Chunking.Overlap,
			SplitMarkers: view.Chunking.SplitMarkers, MinSize: view.Chunking.MinSize,
		},
		Chat: httpapi.ReadOnlyChat{Source: view.Chat.Source, Model: view.Chat.Model},
		Rerank: httpapi.ReadOnlyRerank{Source: view.Rerank.Source, Model: view.Rerank.Model},
		Parser: httpapi.ReadOnlyParser{Version: view.Parser.Version, SupportedFormats: cloneStrings(view.Parser.SupportedFormats)},
	}
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
