package ingestion

import (
	"time"

	"github.com/hzc19970630/rag-platform/contracts/tasks"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/status"
)

const (
	SchemaSourceObject       = "SourceObjectV1"
	SchemaNormalizedDocument = "NormalizedDocumentV1"
	SchemaEvidencePackage    = evidence.Schema
	SchemaTask               = tasks.SchemaIngestion

	TypeTask = tasks.TypeIngestion

	PipelineTypeEvidence = "evidence"

	StageParse     = "parse"
	StageNormalize = "normalize"
	StageChunk     = "chunk"
	StageEmbed     = "embed"
	StageIndex     = "index"

	StatusReceived      = status.Received
	StatusStored        = status.Stored
	StatusParsing       = status.Parsing
	StatusParsed        = status.Parsed
	StatusNormalizing   = status.Normalizing
	StatusNormalized    = status.Normalized
	StatusChunking      = status.Chunking
	StatusChunked       = status.Chunked
	StatusEmbedding     = status.Embedding
	StatusEmbedded      = status.Embedded
	StatusIndexing      = status.Indexing
	StatusEvidenceReady = status.EvidenceReady
	StatusFailed        = status.Failed
	StatusCancelled     = status.Cancelled
	StatusStale         = status.Stale
	StatusParent        = status.Parent

	StagePending    = status.Pending
	StageProcessing = status.Processing
	StageSucceeded  = status.Succeeded
	StageFailed     = status.Failed
	StageCancelled  = status.Cancelled
)

type Envelope = evidence.Envelope

type Task = tasks.Ingestion

type SourceObject struct {
	SchemaVersion string `json:"schema_version"`
	Envelope
	ObjectKey string `json:"object_key"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
}

type NormalizedDocument struct {
	SchemaVersion string `json:"schema_version"`
	Envelope
	Title         string          `json:"title"`
	Language      string          `json:"language"`
	ParserVersion string          `json:"parser_version"`
	Blocks        []DocumentBlock `json:"blocks"`
	Sheets        []SheetMeta     `json:"sheets,omitempty"`
	StorageMode   string          `json:"storage_mode,omitempty"`
	Truncated     bool            `json:"truncated,omitempty"`
	TotalRows     int64           `json:"total_rows,omitempty"`
	IndexedRows   int64           `json:"indexed_rows,omitempty"`
	TotalSheets   int             `json:"total_sheets,omitempty"`
	IndexedSheets int             `json:"indexed_sheets,omitempty"`
	SkippedImages int             `json:"skipped_images,omitempty"`
	SkippedCharts int             `json:"skipped_charts,omitempty"`
	SkippedNotes  int             `json:"skipped_notes,omitempty"`
	// Materialize records the Parquet copy written during parsing, keyed by
	// sheet name. The chunk stage applies it to table assets; it never enters
	// the block stream.
	Materialize map[string]TableMaterializeResult `json:"materialize,omitempty"`
}

// TableRowBatch is one bounded slice of full sheet rows produced by docreader.
type TableRowBatch struct {
	Sheet       string
	Header      []string
	ColumnTypes []string
	RowStart    int64
	Rows        [][]string
	ParsedRows  int64
	SheetDone   bool
	Truncated   bool
	ContentHash string
}

// TableMaterializeTarget identifies the Parquet copy location.
type TableMaterializeTarget struct {
	TenantID        string
	KnowledgeBaseID string
	DocumentID      string
	VersionID       string
}

// TableMaterializeResult is the materializer's outcome for one sheet.
type TableMaterializeResult struct {
	Rows        int64      `json:"rows,omitempty"`
	Bytes       int64      `json:"bytes,omitempty"`
	Status      string     `json:"status,omitempty"`
	Error       string     `json:"error,omitempty"`
	URI         string     `json:"uri,omitempty"`
	ColumnTypes []string   `json:"column_types,omitempty"`
	ContentHash string     `json:"content_hash,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

const (
	MaterializeStatusReady   = "ready"
	MaterializeStatusPartial = "partial"
	MaterializeStatusFailed  = "failed"
)

// TableSheetError is a per-sheet materializer rejection (invalid identifier,
// row valve). The worker records it and keeps the rest of the document.
type TableSheetError struct {
	Status  int
	Message string
}

func (e *TableSheetError) Error() string { return e.Message }

// SheetMeta is the per-sheet catalog entry. It exists even when the sheet is
// only sampled or catalogued, so missing rows are visible instead of silently
// disappearing from coverage.
type SheetMeta struct {
	Name         string   `json:"name"`
	Header       []string `json:"header,omitempty"`
	DeclaredRows int64    `json:"declared_rows,omitempty"`
	TotalRows    int64    `json:"total_rows,omitempty"`
	ParsedRows   int64    `json:"parsed_rows,omitempty"`
	ColumnCount  int      `json:"column_count,omitempty"`
	HeaderRow    int      `json:"header_row,omitempty"`
	ParseStatus  string   `json:"parse_status,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	SampleRows   []string `json:"sample_rows,omitempty"`
	SourcePath   string   `json:"source_path,omitempty"`
	StatsSource  string   `json:"stats_source,omitempty"`
}

type TableMeta struct {
	Sheet       string   `json:"sheet,omitempty"`
	Header      []string `json:"header,omitempty"`
	RowStart    int      `json:"row_start,omitempty"`
	RowEnd      int      `json:"row_end,omitempty"`
	ColumnCount int      `json:"column_count,omitempty"`
}

type DocumentBlock struct {
	BlockID     string     `json:"block_id"`
	Kind        string     `json:"kind"`
	Text        string     `json:"text"`
	HeadingPath string     `json:"heading_path"`
	PageNumber  int        `json:"page_number"`
	SourcePath  string     `json:"source_path"`
	StartOffset int        `json:"start_offset"`
	EndOffset   int        `json:"end_offset"`
	Table       *TableMeta `json:"table,omitempty"`
}

type Chunk = evidence.Chunk

type EvidencePackage = evidence.Package

type ParseResult struct {
	SchemaVersion     string
	DocumentVersionID string
	ContentHash       string
	MimeType          string
	Title             string
	Language          string
	ParserVersion     string
	ErrorCode         string
	ErrorMessage      string
	Blocks            []DocumentBlock
	Sheets            []SheetMeta
	StorageMode       string
	Truncated         bool
	TotalRows         int64
	IndexedRows       int64
	TotalSheets       int
	IndexedSheets     int
	SkippedImages     int
	SkippedCharts     int
	SkippedNotes      int
}
