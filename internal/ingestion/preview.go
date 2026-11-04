package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

const (
	// SchemaDocumentPreview, SchemaChunkPage and SchemaDocumentPage are read-only
	// 展示 schemas. They only project NormalizedDocument/EvidencePackage data and
	// never write back.
	SchemaDocumentPreview = "DocumentPreviewV1"
	SchemaChunkPage       = "ChunkPageV1"
	SchemaDocumentPage    = "DocumentPageV1"

	defaultPageLimit = 50
	maxPageLimit     = 200
)

// DocumentPreview is DocumentPreviewV1: the parsed structure of the current
// version. It is only produced when the version is EVIDENCE_READY, so a partial
// parse is never shown as content.
type DocumentPreview struct {
	SchemaVersion   string         `json:"schemaVersion"`
	DocumentID      string         `json:"documentId"`
	VersionID       string         `json:"versionId"`
	KnowledgeBaseID string         `json:"knowledgeBaseId"`
	Filename        string         `json:"filename"`
	MimeType        string         `json:"mimeType"`
	Status          string         `json:"status"`
	Title           string         `json:"title"`
	Language        string         `json:"language"`
	ParserVersion   string         `json:"parserVersion"`
	Blocks          []PreviewBlock `json:"blocks"`
	Visibility      string         `json:"visibility"`
}

// PreviewBlock is one structured text block with its source position.
type PreviewBlock struct {
	BlockID     string `json:"blockId"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	HeadingPath string `json:"headingPath"`
	PageNumber  int    `json:"pageNumber"`
	SourcePath  string `json:"sourcePath"`
	StartOffset int    `json:"startOffset"`
	EndOffset   int    `json:"endOffset"`
}

// ChunkPage is ChunkPageV1: the retrievable child chunks of the current version
// with cursor pagination. Parent chunks are excluded because their text is the
// aggregation of their own children, so listing both would repeat the document
// text; a parent stays reachable through ChunkDetail by id.
type ChunkPage struct {
	SchemaVersion string      `json:"schemaVersion"`
	DocumentID    string      `json:"documentId"`
	VersionID     string      `json:"versionId"`
	Status        string      `json:"status"`
	Items         []ChunkView `json:"items"`
	NextCursor    int         `json:"nextCursor"`
	HasMore       bool        `json:"hasMore"`
}

// ChunkView is one chunk as shown in the document detail page and in citation
// expansion.
type ChunkView struct {
	ChunkID           string `json:"chunkId"`
	DocumentID        string `json:"documentId"`
	DocumentVersionID string `json:"documentVersionId"`
	KnowledgeBaseID   string `json:"knowledgeBaseId"`
	ParentID          string `json:"parentId,omitempty"`
	Ordinal           int    `json:"ordinal"`
	Content           string `json:"content"`
	ContentHash       string `json:"contentHash"`
	HeadingPath       string `json:"headingPath"`
	PageNumber        int    `json:"pageNumber"`
	SourcePath        string `json:"sourcePath"`
	StartOffset       int    `json:"startOffset"`
	EndOffset         int    `json:"endOffset"`
	Status            string `json:"status"`
}

// DocumentVersionSummary is the version projection DocumentPageV1 exposes. Unlike
// the document status view it deliberately omits the object storage keys: a list
// response must not hand out source or normalized object addresses.
type DocumentVersionSummary struct {
	ID           string `json:"id"`
	Version      int    `json:"version"`
	SizeBytes    int64  `json:"sizeBytes"`
	Status       string `json:"status"`
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
}

// DocumentSummary is one row of DocumentPageV1. It carries the current version
// because the list has to show the pipeline state from the version: on failure
// the aggregate documents.status stays at STORED.
type DocumentSummary struct {
	Document     models.Document        `json:"document"`
	Version      DocumentVersionSummary `json:"version"`
	ContentReady bool                   `json:"contentReady"`
	Previewable  bool                   `json:"previewable"`
}

// DocumentPage is DocumentPageV1: the documents of one knowledge base, newest
// first, with cursor pagination.
type DocumentPage struct {
	SchemaVersion   string            `json:"schemaVersion"`
	KnowledgeBaseID string            `json:"knowledgeBaseId"`
	Items           []DocumentSummary `json:"items"`
	NextCursor      string            `json:"nextCursor"`
	HasMore         bool              `json:"hasMore"`
}

// SourceDocument is the controlled original-file stream. The object key is
// never returned to the client; the API proxies the bytes.
type SourceDocument struct {
	Filename string
	MimeType string
	Content  []byte
}

// DocumentStatusView describes a document whose content is not yet available, so
// the UI can show progress instead of an empty page.
type DocumentStatusView struct {
	Document     models.Document        `json:"document"`
	Version      models.DocumentVersion `json:"version"`
	Pipeline     models.PipelineRun     `json:"pipeline"`
	Stages       []models.StageRun      `json:"stages"`
	ContentReady bool                   `json:"contentReady"`
	Previewable  bool                   `json:"previewable"`
}

// Preview returns DocumentPreviewV1 for the current version.
func (s *Service) Preview(ctx context.Context, principal authz.Principal, documentID string) (DocumentPreview, error) {
	document, version, err := s.authorizedCurrentVersion(ctx, principal, documentID)
	if err != nil {
		return DocumentPreview{}, err
	}
	if version.Status != StatusEvidenceReady {
		return DocumentPreview{}, apperror.ErrNotReady
	}
	if version.NormalizedKey == "" {
		return DocumentPreview{}, apperror.ErrNotReady
	}
	raw, err := s.objects.Get(ctx, version.NormalizedKey)
	if err != nil {
		return DocumentPreview{}, err
	}
	var normalized NormalizedDocument
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return DocumentPreview{}, fmt.Errorf("decode normalized document: %w", err)
	}
	blocks := make([]PreviewBlock, 0, len(normalized.Blocks))
	for _, block := range normalized.Blocks {
		blocks = append(blocks, PreviewBlock{
			BlockID: block.BlockID, Kind: block.Kind, Text: block.Text, HeadingPath: block.HeadingPath,
			PageNumber: block.PageNumber, SourcePath: block.SourcePath, StartOffset: block.StartOffset, EndOffset: block.EndOffset,
		})
	}
	return DocumentPreview{
		SchemaVersion: SchemaDocumentPreview, DocumentID: document.ID, VersionID: version.ID,
		KnowledgeBaseID: document.KnowledgeBaseID, Filename: document.Filename, MimeType: document.MimeType,
		Status: version.Status, Title: normalized.Title, Language: normalized.Language,
		ParserVersion: normalized.ParserVersion, Blocks: blocks, Visibility: "authorized",
	}, nil
}

// Chunks returns ChunkPageV1 for the current version.
func (s *Service) Chunks(ctx context.Context, principal authz.Principal, documentID string, cursor, limit int) (ChunkPage, error) {
	document, version, err := s.authorizedCurrentVersion(ctx, principal, documentID)
	if err != nil {
		return ChunkPage{}, err
	}
	if version.Status != StatusEvidenceReady {
		return ChunkPage{}, apperror.ErrNotReady
	}
	limit = clampPageLimit(limit)
	var rows []models.Chunk
	// Child chunks are the retrievable units and carry a globally unique ordinal,
	// so cursor pagination over them neither repeats nor skips. Parent chunks sit
	// in a separate ordinal space and their text is the aggregation of their
	// children, so including them would both duplicate the document text and
	// break the cursor.
	query := s.db.WithContext(ctx).Where("document_version_id = ? AND tenant_id = ? AND knowledge_base_id = ? AND parent_id IS NOT NULL AND status = ?",
		version.ID, principal.TenantID, document.KnowledgeBaseID, StatusEvidenceReady)
	if cursor > 0 {
		query = query.Where("ordinal > ?", cursor)
	}
	if err := query.Order("ordinal, id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return ChunkPage{}, fmt.Errorf("list chunks: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]ChunkView, 0, len(rows))
	nextCursor := cursor
	for _, row := range rows {
		items = append(items, chunkView(row, document.ID))
		if row.Ordinal > nextCursor {
			nextCursor = row.Ordinal
		}
	}
	return ChunkPage{SchemaVersion: SchemaChunkPage, DocumentID: document.ID, VersionID: version.ID, Status: version.Status, Items: items, NextCursor: nextCursor, HasMore: hasMore}, nil
}

// ListDocuments returns DocumentPageV1 for one knowledge base. Soft-deleted
// documents are excluded: Delete only flips documents.status to CANCELLED and
// leaves the row (and its versions) in place.
func (s *Service) ListDocuments(ctx context.Context, principal authz.Principal, knowledgeBaseID, cursor string, limit int) (DocumentPage, error) {
	if _, err := s.authorizedKnowledgeBase(ctx, principal, knowledgeBaseID, "read"); err != nil {
		return DocumentPage{}, err
	}
	limit = clampPageLimit(limit)
	query := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND status <> ? AND deleted_at IS NULL",
		principal.TenantID, knowledgeBaseID, StatusCancelled)
	if cursor != "" {
		before, err := decodeDocumentCursor(cursor)
		if err != nil {
			return DocumentPage{}, err
		}
		// Keyset comparison over the pair, written out instead of as a row
		// constructor so PostgreSQL can infer the placeholder types.
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", before.CreatedAt, before.CreatedAt, before.ID)
	}
	var rows []models.Document
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return DocumentPage{}, fmt.Errorf("list documents: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	versions, err := s.latestVersions(ctx, rows)
	if err != nil {
		return DocumentPage{}, err
	}
	page := DocumentPage{
		SchemaVersion: SchemaDocumentPage, KnowledgeBaseID: knowledgeBaseID,
		Items: make([]DocumentSummary, 0, len(rows)), HasMore: hasMore,
	}
	for _, row := range rows {
		version := versions[row.LatestVersionID]
		page.Items = append(page.Items, DocumentSummary{
			Document: row,
			Version: DocumentVersionSummary{
				ID: version.ID, Version: version.Version, SizeBytes: version.SizeBytes,
				Status: version.Status, ErrorCode: version.ErrorCode, ErrorMessage: version.ErrorMessage,
			},
			ContentReady: version.Status == StatusEvidenceReady,
			Previewable:  version.Status != StatusCancelled && version.SourceObjectKey != "",
		})
	}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = encodeDocumentCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// latestVersions loads the current versions of one page of documents in a single
// query instead of one query per row.
func (s *Service) latestVersions(ctx context.Context, documents []models.Document) (map[string]models.DocumentVersion, error) {
	ids := make([]string, 0, len(documents))
	for _, document := range documents {
		if document.LatestVersionID != "" {
			ids = append(ids, document.LatestVersionID)
		}
	}
	versions := make(map[string]models.DocumentVersion, len(ids))
	if len(ids) == 0 {
		return versions, nil
	}
	var rows []models.DocumentVersion
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load document versions: %w", err)
	}
	for _, row := range rows {
		versions[row.ID] = row
	}
	return versions, nil
}

// clampPageLimit applies the paging bounds shared by ChunkPageV1 and
// DocumentPageV1: out-of-range values fall back to the default page size.
func clampPageLimit(limit int) int {
	if limit <= 0 || limit > maxPageLimit {
		return defaultPageLimit
	}
	return limit
}

// documentCursor is the keyset position of DocumentPageV1.
type documentCursor struct {
	CreatedAt time.Time
	ID        string
}

func encodeDocumentCursor(createdAt time.Time, id string) string {
	return fmt.Sprintf("%d-%s", createdAt.UTC().UnixNano(), id)
}

func decodeDocumentCursor(raw string) (documentCursor, error) {
	separator := strings.IndexByte(raw, '-')
	if separator <= 0 || separator == len(raw)-1 {
		return documentCursor{}, fmt.Errorf("cursor must be a valid document pagination cursor")
	}
	nanos, err := strconv.ParseInt(raw[:separator], 10, 64)
	if err != nil {
		return documentCursor{}, fmt.Errorf("cursor must be a valid document pagination cursor")
	}
	return documentCursor{CreatedAt: time.Unix(0, nanos).UTC(), ID: raw[separator+1:]}, nil
}

// ChunkDetail resolves a single chunk by id. The chunk id alone is never
// enough: the owning document, version and knowledge base are reloaded and the
// knowledge base read permission is re-checked.
func (s *Service) ChunkDetail(ctx context.Context, principal authz.Principal, chunkID string) (ChunkView, error) {
	if strings.TrimSpace(chunkID) == "" {
		return ChunkView{}, apperror.ErrNotFound
	}
	var chunk models.Chunk
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", principal.TenantID, chunkID).First(&chunk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ChunkView{}, apperror.ErrNotFound
		}
		return ChunkView{}, fmt.Errorf("load chunk: %w", err)
	}
	var version models.DocumentVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", chunk.DocumentVersionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ChunkView{}, apperror.ErrNotFound
		}
		return ChunkView{}, fmt.Errorf("load chunk version: %w", err)
	}
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", principal.TenantID, version.DocumentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ChunkView{}, apperror.ErrNotFound
		}
		return ChunkView{}, fmt.Errorf("load chunk document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "read"); err != nil {
		return ChunkView{}, err
	}
	// A chunk from a superseded, deleted or cancelled version must not be
	// reachable, even with a valid id.
	if document.LatestVersionID != version.ID || version.Status != StatusEvidenceReady {
		return ChunkView{}, apperror.ErrNotReady
	}
	if chunk.Status != StatusParent && chunk.Status != StatusEvidenceReady {
		return ChunkView{}, apperror.ErrNotReady
	}
	return chunkView(chunk, document.ID), nil
}

// SourceDocument streams the original file through the API. The caller must be
// able to read the knowledge base; the object storage key stays server side.
func (s *Service) SourceDocument(ctx context.Context, principal authz.Principal, documentID string) (SourceDocument, error) {
	document, version, err := s.authorizedCurrentVersion(ctx, principal, documentID)
	if err != nil {
		return SourceDocument{}, err
	}
	if version.SourceObjectKey == "" {
		return SourceDocument{}, apperror.ErrNotFound
	}
	content, err := s.objects.Get(ctx, version.SourceObjectKey)
	if err != nil {
		return SourceDocument{}, err
	}
	return SourceDocument{Filename: document.Filename, MimeType: document.MimeType, Content: content}, nil
}

// Status returns the processing status plus the flags the detail page needs to
// decide between "show progress", "show content" and "show failure".
func (s *Service) Status(ctx context.Context, principal authz.Principal, documentID string) (DocumentStatusView, error) {
	document, version, err := s.authorizedCurrentVersion(ctx, principal, documentID)
	if err != nil {
		return DocumentStatusView{}, err
	}
	var pipeline models.PipelineRun
	if err := s.db.WithContext(ctx).Where("document_version_id = ?", version.ID).Order("created_at DESC").First(&pipeline).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return DocumentStatusView{}, fmt.Errorf("load pipeline: %w", err)
		}
	}
	var stages []models.StageRun
	if pipeline.ID != "" {
		if err := s.db.WithContext(ctx).Where("pipeline_run_id = ?", pipeline.ID).Order("id").Find(&stages).Error; err != nil {
			return DocumentStatusView{}, fmt.Errorf("load stages: %w", err)
		}
	}
	return DocumentStatusView{
		Document: document, Version: version, Pipeline: pipeline, Stages: stages,
		ContentReady: version.Status == StatusEvidenceReady,
		Previewable:  version.Status != StatusCancelled && version.SourceObjectKey != "",
	}, nil
}

// authorizedCurrentVersion resolves a document id to its current version and
// authorizes the knowledge base read. Client-supplied version ids are never
// trusted: M3 only serves the latest version.
func (s *Service) authorizedCurrentVersion(ctx context.Context, principal authz.Principal, documentID string) (models.Document, models.DocumentVersion, error) {
	if strings.TrimSpace(documentID) == "" {
		return models.Document{}, models.DocumentVersion{}, apperror.ErrNotFound
	}
	var document models.Document
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", principal.TenantID, documentID).First(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Document{}, models.DocumentVersion{}, apperror.ErrNotFound
		}
		return models.Document{}, models.DocumentVersion{}, fmt.Errorf("load document: %w", err)
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, document.KnowledgeBaseID, "read"); err != nil {
		return models.Document{}, models.DocumentVersion{}, err
	}
	if document.LatestVersionID == "" {
		return document, models.DocumentVersion{}, apperror.ErrNotReady
	}
	var version models.DocumentVersion
	if err := s.db.WithContext(ctx).First(&version, "id = ?", document.LatestVersionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Document{}, models.DocumentVersion{}, apperror.ErrNotReady
		}
		return models.Document{}, models.DocumentVersion{}, fmt.Errorf("load document version: %w", err)
	}
	return document, version, nil
}

func chunkView(chunk models.Chunk, documentID string) ChunkView {
	return ChunkView{
		ChunkID: chunk.ID, DocumentID: documentID, DocumentVersionID: chunk.DocumentVersionID,
		KnowledgeBaseID: chunk.KnowledgeBaseID, ParentID: chunk.ParentID, Ordinal: chunk.Ordinal,
		Content: chunk.Content, ContentHash: chunk.ContentHash, HeadingPath: chunk.HeadingPath,
		PageNumber: chunk.PageNumber, SourcePath: chunk.SourcePath, StartOffset: chunk.StartOffset,
		EndOffset: chunk.EndOffset, Status: chunk.Status,
	}
}
