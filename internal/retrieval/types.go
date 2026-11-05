// Package retrieval implements the online query plan: hybrid BM25 + vector
// recall, RRF fusion, neighbour expansion, authorization re-check and evidence
// assembly. Every recall query carries an AuthorizationScope so filtering
// happens before results leave the data layer.
package retrieval

import (
	"encoding/json"
)

const (
	SchemaQueryPlan   = "QueryPlanV1"
	SchemaEvidenceSet = "EvidenceSetV1"

	// IntentFact is the only intent M3 implements. The design deliberately keeps
	// an LLM intent agent out of the MVP: fact questions query the evidence index.
	IntentFact = "fact"

	ModeEvidence = "evidence"

	// Channel labels recorded on candidates for diagnostics and evaluation.
	ChannelVector   = "vector"
	ChannelKeyword  = "keyword"
	ChannelBoth     = "both"
	ChannelParent   = "parent"
	ChannelNeighbor = "neighbor"
	ChannelReranked = "reranked"
)

// QueryPlan is the fixed first-phase plan. It records what was searched and the
// capability switches that applied, so an answer can be reproduced.
type QueryPlan struct {
	SchemaVersion             string   `json:"schemaVersion"`
	RequestID                 string   `json:"requestId"`
	TenantID                  string   `json:"tenantId"`
	KnowledgeBaseIDs          []string `json:"knowledgeBaseIds"`
	AuthorizationScopeVersion string   `json:"authorizationScopeVersion"`
	Intent                    string   `json:"intent"`
	Mode                      string   `json:"mode"`
	Rewrite                   string   `json:"rewrite"`
	TopK                      int      `json:"topK"`
	VectorEnabled             bool     `json:"vectorEnabled"`
	KeywordEnabled            bool     `json:"keywordEnabled"`
	RerankEnabled             bool     `json:"rerankEnabled"`
	// Channels and Fusion make the plan executable: the executor runs exactly
	// these recall channels and applies the named fusion strategy.
	Channels             []string `json:"channels,omitempty"`
	Fusion               string   `json:"fusion,omitempty"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
	Params               Params   `json:"params"`
	// HistoryTurns records how much conversation context was supplied. The
	// content is never persisted in the plan.
	HistoryTurns int `json:"historyTurns,omitempty"`
}

// Params is the resolved retrieval parameter set after the three-tier override.
// It is owned by the retrieval domain; the YAML configuration converts into it
// at the application boundary instead of leaking the global config type.
type Params struct {
	TopK             int
	EmbeddingTopK    int
	KeywordTopK      int
	VectorThreshold  float64
	KeywordThreshold float64
	RerankEnabled    bool
	RerankTopK       int
	RerankThreshold  float64
	// VectorEFSearch is the pgvector HNSW candidate-list size for this query.
	// Zero leaves the server default in place.
	VectorEFSearch int
	// VectorOversample multiplies the SQL LIMIT so a selective tenant filter
	// still fills TopK after filtering. One keeps the previous behavior.
	VectorOversample int
	// VectorIterativeScan enables pgvector's relaxed-order iterative scan so a
	// filtered HNSW scan keeps walking until it fills TopK. Exact scans ignore
	// it; false leaves the server default (off) in place.
	VectorIterativeScan  bool
	ContextTokenLimit    int
	AdjacentExpansion    int
	MaxAdjacentChunks    int
	RRFK                 int
	ExpansionScoreFactor float64
}

// Hit is a single recall result as returned by a search adapter.
type Hit struct {
	ChunkID           string
	DocumentID        string
	DocumentVersionID string
	KnowledgeBaseID   string
	ParentID          string
	Ordinal           int
	Content           string
	ContentHash       string
	HeadingPath       string
	PageNumber        int
	SourcePath        string
	Score             float64
	Rank              int
	Channel           string
}

// Candidate is a fused evidence candidate with per-channel provenance.
type Candidate struct {
	ChunkID           string   `json:"chunkId"`
	DocumentID        string   `json:"documentId"`
	DocumentVersionID string   `json:"documentVersionId"`
	KnowledgeBaseID   string   `json:"knowledgeBaseId"`
	ParentID          string   `json:"parentId,omitempty"`
	Ordinal           int      `json:"ordinal"`
	Content           string   `json:"content"`
	ContentHash       string   `json:"contentHash"`
	HeadingPath       string   `json:"headingPath"`
	PageNumber        int      `json:"pageNumber"`
	SourcePath        string   `json:"sourcePath"`
	Score             float64  `json:"score"`
	VectorRank        int      `json:"vectorRank,omitempty"`
	KeywordRank       int      `json:"keywordRank,omitempty"`
	VectorScore       float64  `json:"vectorScore,omitempty"`
	KeywordScore      float64  `json:"keywordScore,omitempty"`
	Channels          []string `json:"channels"`
	RerankScore       *float64 `json:"rerankScore,omitempty"`
}

// EvidenceSet is the authorized evidence handed to generation and to citation
// validation. Only items that survived the final authorization re-check appear
// here.
type EvidenceSet struct {
	SchemaVersion             string      `json:"schemaVersion"`
	RequestID                 string      `json:"requestId"`
	AuthorizationScopeVersion string      `json:"authorizationScopeVersion"`
	Query                     string      `json:"query"`
	Plan                      QueryPlan   `json:"plan"`
	Items                     []Candidate `json:"items"`
	TokenEstimate             int         `json:"tokenEstimate"`
	RecallLatencyMS           int64       `json:"recallLatencyMs"`
	RerankApplied             bool        `json:"rerankApplied"`
	DroppedByAuthorization    int         `json:"droppedByAuthorization"`
}

// Relevance reports whether any evidence survived, which the generation layer
// uses to decide between answering and refusing.
func (e EvidenceSet) Relevance() bool { return len(e.Items) > 0 }

// MarshalJSON keeps the stored evidence set compact and stable.
func (e EvidenceSet) Encode() (string, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
