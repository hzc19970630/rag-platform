// Package ports is the dependency-inversion exit for platform adapters.
//
// The domain packages still own the interfaces, but adapters must not import
// the whole ingestion/retrieval package just to name a port type. This package
// re-exports exactly the symbols adapters need; the dependency gate allows it
// and rejects direct adapter -> domain imports.
package ports

import (
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/retrieval"
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
	EvidenceIndexWriter = ingestion.EvidenceIndexWriter
	EmbeddingRecord     = ingestion.EmbeddingRecord
	ChunkDocument       = ingestion.ChunkDocument
)

const (
	ChannelVector   = retrieval.ChannelVector
	ChannelKeyword  = retrieval.ChannelKeyword
	ChannelParent   = retrieval.ChannelParent
	ChannelNeighbor = retrieval.ChannelNeighbor
	ChannelReranked = retrieval.ChannelReranked

	SchemaSourceObject = ingestion.SchemaSourceObject
	StatusEmbedded     = ingestion.StatusEmbedded
)

func SortCandidates(candidates []Candidate) {
	retrieval.SortCandidates(candidates)
}
