// Package ports is the dependency-inversion exit for platform adapters.
//
// The domain packages still own the interfaces, but adapters must not import
// the whole ingestion/retrieval package just to name a port type. This package
// re-exports exactly the symbols adapters need; the dependency gate allows it
// and rejects direct adapter -> domain imports.
package ports

import (
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

type (
	RecallRequest    = retrieval.RecallRequest
	Hit              = retrieval.Hit
	Candidate        = retrieval.Candidate
	ExpansionOptions = retrieval.ExpansionOptions
	KeywordSearcher  = retrieval.KeywordSearcher
	VectorSearcher   = retrieval.VectorSearcher
	ChunkExpander    = retrieval.ChunkExpander
	Reranker         = retrieval.Reranker
	ModelReranker    = retrieval.ModelReranker

	DocumentReader      = ingestion.DocumentReader
	SourceObject        = ingestion.SourceObject
	ParseResult         = ingestion.ParseResult
	DocumentBlock       = ingestion.DocumentBlock
	TableMeta           = ingestion.TableMeta
	SheetMeta           = ingestion.SheetMeta
	EvidenceIndexWriter = ingestion.EvidenceIndexWriter
	EmbeddingRecord     = ingestion.EmbeddingRecord
	ChunkDocument       = ingestion.ChunkDocument
	MultipartPart       = ingestion.MultipartPart
	MultipartStore      = ingestion.MultipartStore
)

const (
	ChannelVector   = retrieval.ChannelVector
	ChannelKeyword  = retrieval.ChannelKeyword
	ChannelParent   = retrieval.ChannelParent
	ChannelNeighbor = retrieval.ChannelNeighbor
	ChannelReranked = retrieval.ChannelReranked

	SchemaSourceObject = ingestion.SchemaSourceObject
	StatusEmbedded     = ingestion.StatusEmbedded
	StatusCancelled    = ingestion.StatusCancelled
)

func SortCandidates(candidates []Candidate) {
	retrieval.SortCandidates(candidates)
}
