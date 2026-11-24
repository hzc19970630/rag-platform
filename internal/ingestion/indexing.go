package ingestion

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
)

// IndexEstimate is the pre-flight view for switching a document's table index
// mode. Minutes are zero when no TPM budget is configured.
type SheetCoverage struct {
	Sheet             string     `json:"sheet"`
	StorageMode       string     `json:"storageMode"`
	ParseStatus       string     `json:"parseStatus"`
	IndexMode         string     `json:"indexMode"`
	TotalRows         int64      `json:"totalRows"`
	ParsedRows        int64      `json:"parsedRows"`
	ColumnCount       int        `json:"columnCount"`
	Truncated         bool       `json:"truncated"`
	MaterializeStatus string     `json:"materializeStatus"`
	MaterializeError  string     `json:"materializeError"`
	MaterializedRows  int64      `json:"materializedRows"`
	ByteSize          int64      `json:"byteSize"`
	MaterializedAt    *time.Time `json:"materializedAt,omitempty"`
}

type IndexEstimate struct {
	DocumentID               string          `json:"documentId"`
	DocumentVersionID        string          `json:"documentVersionId"`
	IndexMode                string          `json:"indexMode"`
	StorageMode              string          `json:"storageMode"`
	ParseStatus              string          `json:"parseStatus"`
	ParseTruncated           bool            `json:"parseTruncated"`
	CoverageKnown            bool            `json:"coverageKnown"`
	TableCount               int64           `json:"tableCount"`
	TotalRows                int64           `json:"totalRows"`
	ParsedRows               int64           `json:"parsedRows"`
	KeywordRows              int64           `json:"keywordRows"`
	VectorRows               int64           `json:"vectorRows"`
	TotalSheets              int             `json:"totalSheets"`
	ParsedSheets             int             `json:"parsedSheets"`
	VectorSheets             int             `json:"vectorSheets"`
	CurrentVectorTokens      int64           `json:"currentVectorTokens"`
	DeepEstimatedTokens      int64           `json:"deepEstimatedTokens"`
	DeepEstimatedMinutes     float64         `json:"deepEstimatedMinutes"`
	TPMBudget                int64           `json:"tpmBudget"`
	MaterializeReadySheets   int             `json:"materializeReadySheets"`
	MaterializePartialSheets int             `json:"materializePartialSheets"`
	MaterializeFailedSheets  int             `json:"materializeFailedSheets"`
	MaterializeMissingSheets int             `json:"materializeMissingSheets"`
	MaterializeRows          int64           `json:"materializeRows"`
	MaterializeBytes         int64           `json:"materializeBytes"`
	Sheets                   []SheetCoverage `json:"sheets"`
}

// IndexEstimate describes what a deep index would cost for the current version.
func (s *Service) IndexEstimate(ctx context.Context, principal authz.Principal, documentID string) (IndexEstimate, error) {
	document, version, err := s.authorizedCurrentVersion(ctx, principal, documentID)
	if err != nil {
		return IndexEstimate{}, err
	}
	estimate := IndexEstimate{
		DocumentID: document.ID, DocumentVersionID: version.ID, IndexMode: version.IndexMode,
		StorageMode: version.StorageMode, ParseStatus: version.ParseStatus, ParseTruncated: version.ParseTruncated,
		CoverageKnown: version.CoverageKnown, TotalRows: version.TotalRows, ParsedRows: version.ParsedRows,
		KeywordRows: version.KeywordRows, VectorRows: version.VectorRows,
		TotalSheets: version.TotalSheets, ParsedSheets: version.ParsedSheets, VectorSheets: version.VectorSheets,
		CurrentVectorTokens: version.VectorTokens, TPMBudget: s.embeddingTPM,
	}
	if estimate.StorageMode == "" {
		estimate.StorageMode = StorageModeFull
	}
	if estimate.ParseStatus == "" {
		estimate.ParseStatus = ParseStatusParsed
	}
	var assets []models.TableAsset
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.TableAsset{}).
		Where("document_version_id = ?", version.ID).Order("sheet, id").Find(&assets).Error; err != nil {
		return IndexEstimate{}, err
	}
	estimate.TableCount = int64(len(assets))
	for _, asset := range assets {
		switch asset.MaterializeStatus {
		case MaterializeStatusReady:
			estimate.MaterializeReadySheets++
		case MaterializeStatusPartial:
			estimate.MaterializePartialSheets++
		case MaterializeStatusFailed:
			estimate.MaterializeFailedSheets++
		default:
			estimate.MaterializeMissingSheets++
		}
		estimate.MaterializeRows += asset.MaterializedRows
		estimate.MaterializeBytes += asset.ByteSize
		estimate.Sheets = append(estimate.Sheets, SheetCoverage{
			Sheet: asset.Sheet, StorageMode: asset.StorageMode, ParseStatus: asset.ParseStatus,
			IndexMode: asset.IndexMode, TotalRows: asset.TotalRows, ParsedRows: asset.ParsedRows,
			ColumnCount: asset.ColumnCount, Truncated: asset.Truncated,
			MaterializeStatus: asset.MaterializeStatus, MaterializeError: asset.MaterializeError,
			MaterializedRows: asset.MaterializedRows, ByteSize: asset.ByteSize, MaterializedAt: asset.MaterializedAt,
		})
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
	if mode == IndexModeDeep && document.LatestVersionID != "" {
		var version models.DocumentVersion
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
			Where("id = ?", document.LatestVersionID).First(&version).Error; err != nil {
			return UploadResult{}, fmt.Errorf("load document version: %w", err)
		}
		if version.StorageMode != "" && version.StorageMode != StorageModeFull {
			return UploadResult{}, apperror.Invalidf(
				"deep_index_requires_full_storage: current document is %s; external query is required for all rows",
				version.StorageMode,
			)
		}
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
