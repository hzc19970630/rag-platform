package views

import (
	"encoding/json"

	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/quality"
)

// RetrievalQualitySummary maps the feedback aggregate to the ops wire shape.
func RetrievalQualitySummary(summary quality.Summary) httpapi.RetrievalQualitySummary {
	top := make([]httpapi.RetrievalQualityChunk, 0, len(summary.TopUnhelpful))
	for _, item := range summary.TopUnhelpful {
		top = append(top, httpapi.RetrievalQualityChunk{ChunkID: item.ChunkID, Count: item.Count})
	}
	result := httpapi.RetrievalQualitySummary{
		Helpful: summary.Helpful, Unhelpful: summary.Unhelpful, CitationClicks: summary.CitationClicks,
		HelpfulRate: summary.HelpfulRate, Messages: summary.Messages, TopUnhelpful: top, Since: summary.Since,
		QualityStatus: summary.QualityStatus,
	}
	if summary.LatestEval != nil {
		latest := RetrievalEvalRun(*summary.LatestEval)
		result.LatestEval = &latest
	}
	return result
}

// RetrievalEvalRun maps one stored evaluation run to the operator wire shape.
func RetrievalEvalRun(row models.RetrievalEvalRun) httpapi.RetrievalEvalRun {
	var metrics map[string]float64
	_ = json.Unmarshal([]byte(row.Metrics), &metrics)
	return httpapi.RetrievalEvalRun{
		ID: row.ID, KnowledgeBaseID: row.KnowledgeBaseID, Kind: row.Kind, Status: row.Status,
		ConfigHash: row.ConfigHash, Metrics: metrics, CreatedAt: row.CreatedAt,
	}
}
