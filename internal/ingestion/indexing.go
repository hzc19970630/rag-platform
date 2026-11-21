package ingestion

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
)

// IndexEstimate is the pre-flight view for switching a document's table index
// mode. Minutes are zero when no TPM budget is configured.
type IndexEstimate struct {
	DocumentID           string  `json:"documentId"`
	DocumentVersionID    string  `json:"documentVersionId"`
	IndexMode            string  `json:"indexMode"`
	TableCount           int64   `json:"tableCount"`
	TotalRows            int64   `json:"totalRows"`
	VectorRows           int64   `json:"vectorRows"`
	TotalSheets          int     `json:"totalSheets"`
	VectorSheets         int     `json:"vectorSheets"`
	CurrentVectorTokens  int64   `json:"currentVectorTokens"`
	DeepEstimatedTokens  int64   `json:"deepEstimatedTokens"`
	DeepEstimatedMinutes float64 `json:"deepEstimatedMinutes"`
	TPMBudget            int64   `json:"tpmBudget"`
}

// IndexEstimate describes what a deep index would cost for the current version.
func (s *Service) IndexEstimate(ctx context.Context, principal authz.Principal, documentID string) (IndexEstimate, error) {
	document, version, err := s.authorizedCurrentVersion(ctx, principal, documentID)
	if err != nil {
		return IndexEstimate{}, err
	}
	estimate := IndexEstimate{
		DocumentID: document.ID, DocumentVersionID: version.ID, IndexMode: version.IndexMode,
		TotalRows: version.TotalRows, VectorRows: version.VectorRows,
		TotalSheets: version.TotalSheets, VectorSheets: version.VectorSheets,
		CurrentVectorTokens: version.VectorTokens, TPMBudget: s.embeddingTPM,
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.TableAsset{}).
		Where("document_version_id = ?", version.ID).Count(&estimate.TableCount).Error; err != nil {
		return IndexEstimate{}, err
	}
	var chars int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Chunk{}).
		Select("COALESCE(SUM(LENGTH(content)), 0)").
		Where("document_version_id = ? AND parent_id IS NOT NULL", version.ID).Scan(&chars).Error; err != nil {
		return IndexEstimate{}, err
	}
	charsPerToken := s.charsPerToken
	if charsPerToken <= 0 {
		charsPerToken = 2
	}
	estimate.DeepEstimatedTokens = (chars + int64(charsPerToken) - 1) / int64(charsPerToken)
	if s.embeddingTPM > 0 && estimate.DeepEstimatedTokens > 0 {
		effective := float64(s.embeddingTPM) * 0.7
		estimate.DeepEstimatedMinutes = math.Ceil(float64(estimate.DeepEstimatedTokens) / effective)
	}
	return estimate, nil
}

// SetIndexMode switches the document's table indexing mode and starts a
// reparse so the new representation is built from the stored source object.
func (s *Service) SetIndexMode(ctx context.Context, principal authz.Principal, documentID, mode string) (UploadResult, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case IndexModeStandard, IndexModeDeep, IndexModeLexical:
	default:
		return UploadResult{}, apperror.Invalidf("unsupported index mode %q", mode)
	}
	document, err := s.loadActiveDocument(ctx, principal, documentID, "write")
	if err != nil {
		return UploadResult{}, err
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "write"); err != nil {
		return UploadResult{}, err
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.Document{}).
		Where("id = ?", document.ID).Update("index_mode", mode).Error; err != nil {
		return UploadResult{}, fmt.Errorf("update index mode: %w", err)
	}
	result, err := s.Reparse(ctx, principal, document.ID)
	if s.metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		s.metrics.Inc("ingestion_index_mode_switch_total", map[string]string{"mode": mode, "status": status})
	}
	if err != nil {
		return UploadResult{}, err
	}
	return result, nil
}
