package views

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func Document(row models.Document) httpapi.Document {
	return httpapi.Document{
		ID:              row.ID,
		TenantID:        row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID,
		OwnerID:         row.OwnerID,
		Filename:        row.Filename,
		MimeType:        row.MimeType,
		Status:          row.Status,
		LatestVersionID: row.LatestVersionID,
		IndexMode:       row.IndexMode,
		FolderID:        row.FolderID,
		DeletedAt:       row.DeletedAt,
		DeletedBy:       row.DeletedBy,
		PurgeAfter:      row.PurgeAfter,
		RestoredAt:      row.RestoredAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func DocumentVersion(row models.DocumentVersion) httpapi.DocumentVersion {
	return httpapi.DocumentVersion{
		ID:              row.ID,
		DocumentID:      row.DocumentID,
		TenantID:        row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID,
		Version:         row.Version,
		ContentHash:     row.ContentHash,
		SizeBytes:       row.SizeBytes,
		Status:          row.Status,
		ACLVersion:      row.ACLVersion,
		IndexMode:       row.IndexMode,
		StorageMode:     row.StorageMode,
		ParseStatus:     row.ParseStatus,
		ParseTruncated:  row.ParseTruncated,
		ParsedSheets:    row.ParsedSheets,
		ParsedRows:      row.ParsedRows,
		KeywordRows:     row.KeywordRows,
		CoverageKnown:   row.CoverageKnown,
		VectorRows:      row.VectorRows,
		TotalRows:       row.TotalRows,
		VectorSheets:    row.VectorSheets,
		TotalSheets:     row.TotalSheets,
		SkippedImages:   row.SkippedImages,
		SkippedCharts:   row.SkippedCharts,
		SkippedNotes:    row.SkippedNotes,
		VectorTokens:    row.VectorTokens,
		EstimatedTokens: row.EstimatedTokens,
		ErrorCode:       row.ErrorCode,
		ErrorMessage:    row.ErrorMessage,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func TableAsset(row models.TableAsset) httpapi.TableAsset {
	var header []string
	_ = json.Unmarshal([]byte(row.Header), &header)
	var schema struct {
		ColumnTypes []string `json:"columnTypes"`
	}
	_ = json.Unmarshal([]byte(row.SchemaJSON), &schema)
	columnTypes := make([]string, len(header))
	for index := range columnTypes {
		columnTypes[index] = "string"
		if index < len(schema.ColumnTypes) && schema.ColumnTypes[index] != "" {
			columnTypes[index] = schema.ColumnTypes[index]
		}
	}
	return httpapi.TableAsset{
		ID: row.ID, DocumentID: row.DocumentID, DocumentVersionID: row.DocumentVersionID,
		Sheet: row.Sheet, Header: header, SQLColumns: sqlColumnNames(header), ColumnTypes: columnTypes,
		RowStart: row.RowStart, RowEnd: row.RowEnd,
		RowCount: row.RowCount, ColumnCount: row.ColumnCount, IndexMode: row.IndexMode,
		StorageMode: row.StorageMode, ParseStatus: row.ParseStatus, Truncated: row.Truncated,
		TotalRows: row.TotalRows, ParsedRows: row.ParsedRows, HeaderRow: row.HeaderRow,
		MaterializeStatus: row.MaterializeStatus, MaterializeError: row.MaterializeError,
		MaterializedRows: row.MaterializedRows, ByteSize: row.ByteSize, MaterializedAt: row.MaterializedAt,
	}
}

// sqlColumnNames is the Go twin of tablehouse's _sql_columns. Both sides must
// agree so a generated statement binds to the same Parquet columns.
func sqlColumnNames(header []string) []string {
	names := make([]string, len(header))
	seen := make(map[string]bool, len(header))
	for index, raw := range header {
		name := strings.TrimSpace(raw)
		if name == "" {
			name = fmt.Sprintf("column_%d", index+1)
		}
		candidate := name
		for suffix := 2; seen[strings.ToLower(candidate)] || strings.EqualFold(candidate, "__row_number"); suffix++ {
			candidate = fmt.Sprintf("%s_%d", name, suffix)
		}
		seen[strings.ToLower(candidate)] = true
		names[index] = candidate
	}
	return names
}

func TableAssets(rows []models.TableAsset) []httpapi.TableAsset {
	items := make([]httpapi.TableAsset, 0, len(rows))
	for _, row := range rows {
		items = append(items, TableAsset(row))
	}
	return items
}

func TableQueryResult(view ingestion.TableQueryResult) httpapi.TableQueryResult {
	rows := make([]httpapi.TableRow, 0, len(view.Rows))
	for _, row := range view.Rows {
		rows = append(rows, httpapi.TableRow{RowNumber: row.RowNumber, Cells: row.Cells})
	}
	return httpapi.TableQueryResult{
		AssetID: view.AssetID, Sheet: view.Sheet, Header: view.Header,
		TotalRows: view.TotalRows, MatchedRows: view.MatchedRows, Rows: rows,
		SourceRows: view.SourceRows, StorageMode: view.StorageMode, ParseStatus: view.ParseStatus, Truncated: view.Truncated,
	}
}

func TableSQLResult(view ingestion.TableSQLResult) httpapi.TableSQLResult {
	columns := make([]httpapi.TableSQLColumn, 0, len(view.Columns))
	for _, column := range view.Columns {
		columns = append(columns, httpapi.TableSQLColumn{Name: column.Name, Type: column.Type})
	}
	rows := view.Rows
	if rows == nil {
		rows = [][]any{}
	}
	return httpapi.TableSQLResult{
		AssetID: view.AssetID, Sheet: view.Sheet, SQL: view.SQL, Preview: view.Preview,
		Columns: columns, Rows: rows, RowCount: view.RowCount, ReturnedRows: view.ReturnedRows,
		Truncated: view.Truncated, ElapsedMs: view.ElapsedMs,
	}
}

func TableAskResult(view ingestion.TableAskResult) httpapi.TableAskResult {
	rows := make([]httpapi.TableRow, 0, len(view.Rows))
	for _, row := range view.Rows {
		rows = append(rows, httpapi.TableRow{RowNumber: row.RowNumber, Cells: row.Cells})
	}
	return httpapi.TableAskResult{
		Route: view.Route, Operation: view.Operation, Confidence: view.Confidence,
		MatchedRules: view.MatchedRules, AssetID: view.AssetID, DocumentID: view.DocumentID,
		Sheet: view.Sheet, Header: view.Header, Column: view.Column, TopN: view.TopN, Desc: view.Desc,
		Value: view.Value, Rows: rows, Sample: view.Sample, MatchedRows: view.MatchedRows,
		TotalRows: view.TotalRows, SourceRows: view.SourceRows, IndexMode: view.IndexMode,
		StorageMode: view.StorageMode, ParseStatus: view.ParseStatus, Truncated: view.Truncated,
		NeedsColumn: view.NeedsColumn, Notes: view.Notes,
	}
}

func TableAskSQLResult(view ingestion.TableAskSQLResult) httpapi.TableAskSQLResult {
	var plan *httpapi.TableAskSQLPlan
	if view.Plan != nil {
		filters := make([]httpapi.TableFilter, 0, len(view.Plan.Filters))
		for _, filter := range view.Plan.Filters {
			filters = append(filters, httpapi.TableFilter{Column: filter.Column, Op: filter.Op, Value: filter.Value})
		}
		plan = &httpapi.TableAskSQLPlan{
			Operation: view.Plan.Operation, Column: view.Plan.Column, Filters: filters,
			OrderBy: view.Plan.OrderBy, Desc: view.Plan.Desc, Limit: view.Plan.Limit, Confidence: view.Plan.Confidence,
		}
	}
	var result *httpapi.TableAskResult
	if view.Result != nil {
		converted := TableAskResult(*view.Result)
		result = &converted
	}
	return httpapi.TableAskSQLResult{
		Preview: view.Preview, SQL: view.SQL, Plan: plan, EstimatedRows: view.EstimatedRows,
		Fallback: view.Fallback, FallbackReason: view.FallbackReason, Result: result,
		MatchedRows: view.MatchedRows, ElapsedMs: view.ElapsedMs, Notes: view.Notes,
	}
}

func DocumentIndexEstimate(view ingestion.IndexEstimate) httpapi.DocumentIndexEstimate {
	sheets := make([]httpapi.SheetCoverage, 0, len(view.Sheets))
	for _, sheet := range view.Sheets {
		sheets = append(sheets, httpapi.SheetCoverage{
			Sheet: sheet.Sheet, StorageMode: sheet.StorageMode, ParseStatus: sheet.ParseStatus,
			IndexMode: sheet.IndexMode, TotalRows: sheet.TotalRows, ParsedRows: sheet.ParsedRows,
			ColumnCount: sheet.ColumnCount, Truncated: sheet.Truncated,
			MaterializeStatus: sheet.MaterializeStatus, MaterializeError: sheet.MaterializeError,
			MaterializedRows: sheet.MaterializedRows, ByteSize: sheet.ByteSize, MaterializedAt: sheet.MaterializedAt,
		})
	}
	return httpapi.DocumentIndexEstimate{
		DocumentID: view.DocumentID, DocumentVersionID: view.DocumentVersionID, IndexMode: view.IndexMode,
		StorageMode: view.StorageMode, ParseStatus: view.ParseStatus, ParseTruncated: view.ParseTruncated,
		CoverageKnown: view.CoverageKnown, TableCount: view.TableCount,
		TotalRows: view.TotalRows, ParsedRows: view.ParsedRows, KeywordRows: view.KeywordRows,
		VectorRows: view.VectorRows, TotalSheets: view.TotalSheets, ParsedSheets: view.ParsedSheets,
		VectorSheets:  view.VectorSheets,
		SkippedImages: view.SkippedImages, SkippedCharts: view.SkippedCharts, SkippedNotes: view.SkippedNotes,
		CurrentVectorTokens: view.CurrentVectorTokens,
		DeepEstimatedTokens: view.DeepEstimatedTokens, DeepEstimatedMinutes: view.DeepEstimatedMinutes,
		TPMBudget:              view.TPMBudget,
		MaterializeReadySheets: view.MaterializeReadySheets, MaterializePartialSheets: view.MaterializePartialSheets,
		MaterializeFailedSheets: view.MaterializeFailedSheets, MaterializeMissingSheets: view.MaterializeMissingSheets,
		MaterializeRows: view.MaterializeRows, MaterializeBytes: view.MaterializeBytes,
		Sheets: sheets,
	}
}

func PipelineRun(row models.PipelineRun) httpapi.PipelineRun {
	return httpapi.PipelineRun{
		ID:                row.ID,
		PipelineType:      row.PipelineType,
		TenantID:          row.TenantID,
		KnowledgeBaseID:   row.KnowledgeBaseID,
		DocumentID:        row.DocumentID,
		DocumentVersionID: row.DocumentVersionID,
		RequestID:         row.RequestID,
		TraceID:           row.TraceID,
		IdempotencyKey:    row.IdempotencyKey,
		Status:            row.Status,
		ErrorCode:         row.ErrorCode,
		ErrorMessage:      row.ErrorMessage,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func StageRun(row models.StageRun) httpapi.StageRun {
	return httpapi.StageRun{
		ID:             row.ID,
		PipelineRunID:  row.PipelineRunID,
		Stage:          row.Stage,
		Status:         row.Status,
		Attempts:       row.Attempts,
		IdempotencyKey: row.IdempotencyKey,
		ErrorCode:      row.ErrorCode,
		ErrorMessage:   row.ErrorMessage,
		StartedAt:      row.StartedAt,
		CompletedAt:    row.CompletedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func StageRuns(rows []models.StageRun) []httpapi.StageRun {
	items := make([]httpapi.StageRun, 0, len(rows))
	for _, row := range rows {
		items = append(items, StageRun(row))
	}
	return items
}

func DocumentView(view ingestion.DocumentView) httpapi.DocumentView {
	return httpapi.DocumentView{
		Document: Document(view.Document),
		Version:  DocumentVersion(view.Version),
		Pipeline: PipelineRun(view.Pipeline),
		Stages:   StageRuns(view.Stages),
	}
}

func UploadResult(view ingestion.UploadResult) httpapi.DocumentView {
	return httpapi.DocumentView{
		Document: Document(view.Document),
		Version:  DocumentVersion(view.Version),
		Pipeline: PipelineRun(view.Pipeline),
		Stages:   StageRuns(view.Stages),
	}
}

func DocumentEnvelope(row models.Document) httpapi.DocumentEnvelope {
	return httpapi.DocumentEnvelope{Document: Document(row)}
}

func DocumentVersionSummary(view ingestion.DocumentVersionSummary) httpapi.DocumentVersionSummary {
	return httpapi.DocumentVersionSummary{
		ID:           view.ID,
		Version:      view.Version,
		SizeBytes:    view.SizeBytes,
		Status:       view.Status,
		ErrorCode:    view.ErrorCode,
		ErrorMessage: view.ErrorMessage,
	}
}

func DocumentSummary(view ingestion.DocumentSummary) httpapi.DocumentSummary {
	return httpapi.DocumentSummary{
		Document:     Document(view.Document),
		Version:      DocumentVersionSummary(view.Version),
		ContentReady: view.ContentReady,
		Previewable:  view.Previewable,
		Tags:         DocumentTagRefs(view.Tags),
	}
}

func DocumentTagRefs(refs []ingestion.TagRef) []httpapi.DocumentTagRef {
	items := make([]httpapi.DocumentTagRef, 0, len(refs))
	for _, ref := range refs {
		items = append(items, httpapi.DocumentTagRef{ID: ref.ID, Name: ref.Name})
	}
	return items
}

func Folder(row models.Folder) httpapi.Folder {
	return httpapi.Folder{
		ID: row.ID, KnowledgeBaseID: row.KnowledgeBaseID, ParentID: defaultString(row.ParentID),
		Name: row.Name, Path: row.Path, Depth: row.Depth,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func FolderList(rows []models.Folder) httpapi.FolderList {
	items := make([]httpapi.Folder, 0, len(rows))
	for _, row := range rows {
		items = append(items, Folder(row))
	}
	return httpapi.FolderList{Items: items}
}

func Tag(row models.Tag) httpapi.Tag {
	return httpapi.Tag{
		ID: row.ID, KnowledgeBaseID: row.KnowledgeBaseID, Name: row.Name,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func TagList(rows []models.Tag) httpapi.TagList {
	items := make([]httpapi.Tag, 0, len(rows))
	for _, row := range rows {
		items = append(items, Tag(row))
	}
	return httpapi.TagList{Items: items}
}

func BatchResults(results []ingestion.BatchResult) httpapi.BatchDocumentsResult {
	items := make([]httpapi.BatchDocumentResult, 0, len(results))
	for _, result := range results {
		items = append(items, httpapi.BatchDocumentResult{
			DocumentID: result.DocumentID, OK: result.OK,
			ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage,
		})
	}
	return httpapi.BatchDocumentsResult{Items: items}
}

func defaultString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func DocumentPage(view ingestion.DocumentPage) httpapi.DocumentPage {
	items := make([]httpapi.DocumentSummary, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, DocumentSummary(item))
	}
	return httpapi.DocumentPage{
		SchemaVersion:   view.SchemaVersion,
		KnowledgeBaseID: view.KnowledgeBaseID,
		Items:           items,
		NextCursor:      view.NextCursor,
		HasMore:         view.HasMore,
	}
}

func Chunk(view ingestion.ChunkView) httpapi.Chunk {
	return httpapi.Chunk{
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
		StartOffset:       view.StartOffset,
		EndOffset:         view.EndOffset,
		Status:            view.Status,
	}
}

func ChunkPage(view ingestion.ChunkPage) httpapi.ChunkPage {
	items := make([]httpapi.Chunk, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, Chunk(item))
	}
	return httpapi.ChunkPage{
		SchemaVersion: view.SchemaVersion,
		DocumentID:    view.DocumentID,
		VersionID:     view.VersionID,
		Status:        view.Status,
		Items:         items,
		NextCursor:    view.NextCursor,
		HasMore:       view.HasMore,
	}
}

func PreviewBlock(view ingestion.PreviewBlock) httpapi.PreviewBlock {
	return httpapi.PreviewBlock{
		BlockID:     view.BlockID,
		Kind:        view.Kind,
		Text:        view.Text,
		HeadingPath: view.HeadingPath,
		PageNumber:  view.PageNumber,
		SourcePath:  view.SourcePath,
		StartOffset: view.StartOffset,
		EndOffset:   view.EndOffset,
	}
}

func DocumentPreview(view ingestion.DocumentPreview) httpapi.DocumentPreview {
	blocks := make([]httpapi.PreviewBlock, 0, len(view.Blocks))
	for _, block := range view.Blocks {
		blocks = append(blocks, PreviewBlock(block))
	}
	return httpapi.DocumentPreview{
		SchemaVersion:   view.SchemaVersion,
		DocumentID:      view.DocumentID,
		VersionID:       view.VersionID,
		KnowledgeBaseID: view.KnowledgeBaseID,
		Filename:        view.Filename,
		MimeType:        view.MimeType,
		Status:          view.Status,
		Title:           view.Title,
		Language:        view.Language,
		ParserVersion:   view.ParserVersion,
		Blocks:          blocks,
		Visibility:      view.Visibility,
		Truncated:       view.Truncated,
		TotalRows:       view.TotalRows,
		IndexedRows:     view.IndexedRows,
		TotalSheets:     view.TotalSheets,
		IndexedSheets:   view.IndexedSheets,
	}
}

func DocumentStatus(view ingestion.DocumentStatusView) httpapi.DocumentStatus {
	return httpapi.DocumentStatus{
		Document:     Document(view.Document),
		Version:      DocumentVersion(view.Version),
		Pipeline:     PipelineRun(view.Pipeline),
		Stages:       StageRuns(view.Stages),
		ContentReady: view.ContentReady,
		Previewable:  view.Previewable,
	}
}

func DocumentVersionView(view ingestion.DocumentVersionView) httpapi.DocumentVersionView {
	return httpapi.DocumentVersionView{
		ID:           view.ID,
		Version:      view.Version,
		ContentHash:  view.ContentHash,
		SizeBytes:    view.SizeBytes,
		Status:       view.Status,
		ErrorCode:    view.ErrorCode,
		ErrorMessage: view.ErrorMessage,
		Current:      view.Current,
		ContentReady: view.ContentReady,
		Restorable:   view.Restorable,
		CreatedAt:    view.CreatedAt,
	}
}

func DocumentVersionPage(view ingestion.DocumentVersionPage) httpapi.DocumentVersionPage {
	items := make([]httpapi.DocumentVersionView, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, DocumentVersionView(item))
	}
	return httpapi.DocumentVersionPage{
		SchemaVersion: view.SchemaVersion,
		DocumentID:    view.DocumentID,
		CurrentID:     view.CurrentID,
		Items:         items,
	}
}
