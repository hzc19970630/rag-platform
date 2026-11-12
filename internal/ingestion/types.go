package ingestion

import (
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
}

type DocumentBlock struct {
	BlockID     string `json:"block_id"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	HeadingPath string `json:"heading_path"`
	PageNumber  int    `json:"page_number"`
	SourcePath  string `json:"source_path"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
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
}
