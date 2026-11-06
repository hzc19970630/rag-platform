package views

import (
	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/retrieval"
)

func EvidenceSet(view retrieval.EvidenceSet) httpapi.EvidenceSet {
	items := make([]httpapi.EvidenceCandidate, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, EvidenceCandidate(item))
	}
	return httpapi.EvidenceSet{
		SchemaVersion:             view.SchemaVersion,
		RequestID:                 view.RequestID,
		AuthorizationScopeVersion: view.AuthorizationScopeVersion,
		Query:                     view.Query,
		Plan:                      QueryPlan(view.Plan),
		Items:                     items,
		TokenEstimate:             view.TokenEstimate,
		RecallLatencyMS:           view.RecallLatencyMS,
		RerankApplied:             view.RerankApplied,
		DroppedByAuthorization:    view.DroppedByAuthorization,
	}
}

func QueryPlan(view retrieval.QueryPlan) httpapi.QueryPlan {
	return httpapi.QueryPlan{
		SchemaVersion:             view.SchemaVersion,
		RequestID:                 view.RequestID,
		TenantID:                  view.TenantID,
		KnowledgeBaseIDs:          cloneStrings(view.KnowledgeBaseIDs),
		AuthorizationScopeVersion: view.AuthorizationScopeVersion,
		Intent:                    view.Intent,
		Mode:                      view.Mode,
		Rewrite:                   view.Rewrite,
		TopK:                      view.TopK,
		VectorEnabled:             view.VectorEnabled,
		KeywordEnabled:            view.KeywordEnabled,
		RerankEnabled:             view.RerankEnabled,
		Channels:                  cloneStrings(view.Channels),
		Fusion:                    view.Fusion,
		RequiredCapabilities:      cloneStrings(view.RequiredCapabilities),
		Params:                    QueryPlanParams(view.Params),
		HistoryTurns:              view.HistoryTurns,
	}
}

func QueryPlanParams(view retrieval.Params) httpapi.QueryPlanParams {
	return httpapi.QueryPlanParams{
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

func EvidenceCandidate(view retrieval.Candidate) httpapi.EvidenceCandidate {
	return httpapi.EvidenceCandidate{
		ChunkID:           view.ChunkID,
		DocumentID:        view.DocumentID,
		DocumentVersionID: view.DocumentVersionID,
		KnowledgeBaseID:   view.KnowledgeBaseID,
		ParentID:          view.ParentID,
		Ordinal:           view.Ordinal,
		Content:           view.Content,
		ContentHash:       view.ContentHash,
		HeadingPath:       view.HeadingPath,
		PageNumber:        view.PageNumber,
		SourcePath:        view.SourcePath,
		Score:             view.Score,
		VectorRank:        view.VectorRank,
		KeywordRank:       view.KeywordRank,
		VectorScore:       view.VectorScore,
		KeywordScore:      view.KeywordScore,
		Channels:          cloneStrings(view.Channels),
		RerankScore:       view.RerankScore,
	}
}
