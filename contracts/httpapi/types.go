// Package httpapi owns the HTTP wire contracts: request bodies, response
// bodies and the route manifest. Transport code binds and emits these types;
// persistence rows never reach the wire, and api/openapi/openapi.yaml is
// generated from this package by cmd/openapi.
package httpapi

import (
	"encoding/json"
	"time"
)

// Error is the shared error body. Field names match apperror responses.
type Error struct {
	Error   string `json:"error" oa:"required"`
	Message string `json:"message,omitempty"`
}

// PrincipalView is the safe projection of an authenticated principal.
type PrincipalView struct {
	SubjectID      string   `json:"subjectId"`
	TenantID       string   `json:"tenantId"`
	Capabilities   []string `json:"capabilities,omitempty"`
	CredentialType string   `json:"credentialType,omitempty"`
	CredentialID   string   `json:"credentialId,omitempty"`
}

// ScopeView is the authorized resource scope resolved for the current request.
type ScopeView struct {
	SubjectID        string   `json:"subjectId"`
	TenantID         string   `json:"tenantId"`
	KnowledgeBaseIDs []string `json:"knowledgeBaseIds"`
	ResourceIDs      []string `json:"resourceIds"`
	Capabilities     []string `json:"capabilities"`
	ACLVersion       string   `json:"aclVersion"`
}

type MeResponse struct {
	Principal           PrincipalView `json:"principal"`
	Scope               ScopeView     `json:"scope"`
	DevelopmentIdentity bool          `json:"developmentIdentity"`
}

type AuthConfig struct {
	OIDCEnabled     bool   `json:"oidcEnabled" oa:"required"`
	OIDCIssuer      string `json:"oidcIssuer"`
	LDAPEnabled     bool   `json:"ldapEnabled" oa:"required"`
	DevAuthEnabled  bool   `json:"devAuthEnabled" oa:"required"`
	SessionCookie   string `json:"sessionCookie" oa:"required"`
	SessionTTLHours int    `json:"sessionTTLHours" oa:"required"`
}

type LDAPLoginRequest struct {
	Username string `json:"username" oa:"required"`
	Password string `json:"password" oa:"required"`
}

type DevLoginRequest struct {
	UserID   string `json:"userId"`
	TenantID string `json:"tenantId"`
}

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type DevLoginResponse struct {
	Token     string        `json:"token" oa:"required"`
	ExpiresAt time.Time     `json:"expiresAt" oa:"required"`
	User      User          `json:"user" oa:"required"`
	Principal PrincipalView `json:"principal" oa:"required"`
}

type RefreshResponse struct {
	Token     string    `json:"token" oa:"required"`
	ExpiresAt time.Time `json:"expiresAt" oa:"required"`
}

type APIKeyCreateRequest struct {
	Name             string     `json:"name" oa:"required"`
	Capabilities     []string   `json:"capabilities"`
	KnowledgeBaseIDs []string   `json:"knowledgeBaseIds"`
	ExpiresAt        *time.Time `json:"expiresAt"`
}

type APIKey struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	KeyPrefix        string     `json:"keyPrefix"`
	Capabilities     []string   `json:"capabilities"`
	KnowledgeBaseIDs []string   `json:"knowledgeBaseIds"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt       *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type APIKeyList struct {
	Items []APIKey `json:"items" oa:"required"`
}

type APIKeyCreateResponse struct {
	APIKey APIKey `json:"apiKey" oa:"required"`
	Token  string `json:"token" oa:"required"`
}

type TenantMember struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type TenantMemberList struct {
	Items []TenantMember `json:"items" oa:"required"`
}

type TenantMemberCreateRequest struct {
	UserID string `json:"userId" binding:"required"`
	Role   string `json:"role" binding:"required"`
}

type TenantMemberUpdateRequest struct {
	Role string `json:"role" binding:"required"`
}

type TenantMemberCounts struct {
	Total   int64 `json:"total"`
	Owners  int64 `json:"owners"`
	Admins  int64 `json:"admins"`
	Members int64 `json:"members"`
	Viewers int64 `json:"viewers"`
}

// TenantOverview is the operator-facing tenant snapshot. Counts exclude
// soft-deleted documents and cover the caller's tenant only.
type TenantOverview struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	CreatedAt          time.Time          `json:"createdAt"`
	Members            TenantMemberCounts `json:"members"`
	KnowledgeBases     int64              `json:"knowledgeBases"`
	Documents          int64              `json:"documents"`
	StorageBytes       int64              `json:"storageBytes"`
	ChatQuestionsToday int64              `json:"chatQuestionsToday"`
}

// TenantQuotaField carries the effective limit plus its origin. Unlimited is
// true when Value is zero; Source is "tenant" or "deployment".
type TenantQuotaField struct {
	Value     int64  `json:"value"`
	Default   int64  `json:"default"`
	Source    string `json:"source"`
	Unlimited bool   `json:"unlimited"`
}

type TenantQuota struct {
	MaxDocuments       TenantQuotaField `json:"maxDocuments"`
	MaxStorageBytes    TenantQuotaField `json:"maxStorageBytes"`
	ChatRequestsPerDay TenantQuotaField `json:"chatRequestsPerDay"`
}

// TenantQuotaUpdateRequest replaces the tenant override object. A null or
// omitted field inherits the deployment default; zero means explicitly
// unlimited. Negative values are rejected.
type TenantQuotaUpdateRequest struct {
	MaxDocuments       *int64 `json:"maxDocuments" oa:"min=0"`
	MaxStorageBytes    *int64 `json:"maxStorageBytes" oa:"min=0"`
	ChatRequestsPerDay *int64 `json:"chatRequestsPerDay" oa:"min=0"`
}

type AuditEvent struct {
	ID           uint64    `json:"id"`
	TenantID     string    `json:"tenantId"`
	ActorID      string    `json:"actorId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Allowed      bool      `json:"allowed"`
	Reason       string    `json:"reason"`
	RequestID    string    `json:"requestId"`
	IPAddress    string    `json:"ipAddress"`
	UserAgent    string    `json:"userAgent"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AuditEventList struct {
	Items []AuditEvent `json:"items" oa:"required"`
}

type KnowledgeBase struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenantId"`
	OwnerID            string    `json:"ownerId"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	EmbeddingModel     string    `json:"embeddingModel"`
	EmbeddingDimension int       `json:"embeddingDimension"`
	ACLVersion         int64     `json:"aclVersion"`
	RetrievalConfig    string    `json:"retrievalConfig"`
	IndexingStrategy   string    `json:"indexingStrategy"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type KnowledgeBaseList struct {
	Items []KnowledgeBase `json:"items"`
}

type KnowledgeBaseCreateRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=255" oa:"maxLength=255"`
	Description string `json:"description"`
}

type KnowledgeBaseUpdateRequest struct {
	Name               *string                  `json:"name" oa:"maxLength=255"`
	Description        *string                  `json:"description"`
	RetrievalConfig    *RetrievalConfigOverride `json:"retrieval_config"`
	IndexingStrategy   *IndexingStrategy        `json:"indexing_strategy"`
	EmbeddingModel     *string                  `json:"embedding_model"`
	EmbeddingDimension *int                     `json:"embedding_dimension"`
	ChatModel          *string                  `json:"chat_model"`
	RerankModel        *string                  `json:"rerank_model"`
}

type KnowledgeBaseMember struct {
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	UserID          string    `json:"userId"`
	Role            string    `json:"role"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type KnowledgeBaseMemberList struct {
	Items []KnowledgeBaseMember `json:"items"`
}

type KnowledgeBaseMemberCreateRequest struct {
	UserID string `json:"user_id" binding:"required"`
	Role   string `json:"role" binding:"required" oa:"enum=viewer|editor"`
}

type RetrievalConfigOverride struct {
	TopK                 *int     `json:"top_k,omitempty"`
	EmbeddingTopK        *int     `json:"embedding_top_k,omitempty"`
	KeywordTopK          *int     `json:"keyword_top_k,omitempty"`
	VectorThreshold      *float64 `json:"vector_threshold,omitempty"`
	KeywordThreshold     *float64 `json:"keyword_threshold,omitempty"`
	RerankEnabled        *bool    `json:"rerank_enabled,omitempty"`
	RerankTopK           *int     `json:"rerank_top_k,omitempty"`
	RerankThreshold      *float64 `json:"rerank_threshold,omitempty"`
	VectorEFSearch       *int     `json:"vector_ef_search,omitempty" oa:"min=0,max=1000"`
	VectorOversample     *int     `json:"vector_oversample,omitempty" oa:"min=1,max=20"`
	VectorIterativeScan  *bool    `json:"vector_iterative_scan,omitempty"`
	ContextTokenLimit    *int     `json:"context_token_limit,omitempty"`
	AdjacentExpansion    *int     `json:"adjacent_expansion,omitempty"`
	MaxAdjacentChunks    *int     `json:"max_adjacent_chunks,omitempty"`
	RRFK                 *int     `json:"rrf_k,omitempty"`
	ExpansionScoreFactor *float64 `json:"expansion_score_factor,omitempty"`
}

// IndexingStrategy mirrors IndexingStrategyV1. It is a contract type so the
// HTTP tier never binds a retrieval-domain struct directly.
type IndexingStrategy struct {
	VectorEnabled        bool     `json:"vector_enabled"`
	KeywordEnabled       bool     `json:"keyword_enabled"`
	WikiEnabled          bool     `json:"wiki_enabled"`
	GraphEnabled         bool     `json:"graph_enabled"`
	RequiredCapabilities []string `json:"required_capabilities"`
}

// RetrievalParams is the resolved retrieval parameter set. The snake_case
// names are the configuration API contract.
type RetrievalParams struct {
	TopK                 int     `json:"top_k"`
	EmbeddingTopK        int     `json:"embedding_top_k"`
	KeywordTopK          int     `json:"keyword_top_k"`
	VectorThreshold      float64 `json:"vector_threshold"`
	KeywordThreshold     float64 `json:"keyword_threshold"`
	RerankEnabled        bool    `json:"rerank_enabled"`
	RerankTopK           int     `json:"rerank_top_k"`
	RerankThreshold      float64 `json:"rerank_threshold"`
	VectorEFSearch       int     `json:"vector_ef_search"`
	VectorOversample     int     `json:"vector_oversample"`
	VectorIterativeScan  bool    `json:"vector_iterative_scan"`
	ContextTokenLimit    int     `json:"context_token_limit"`
	AdjacentExpansion    int     `json:"adjacent_expansion"`
	MaxAdjacentChunks    int     `json:"max_adjacent_chunks"`
	RRFK                 int     `json:"rrf_k"`
	ExpansionScoreFactor float64 `json:"expansion_score_factor"`
}

type RetrievalConfiguration struct {
	GlobalDefaults        RetrievalParams         `json:"globalDefaults"`
	KnowledgeBaseOverride RetrievalConfigOverride `json:"knowledgeBaseOverride"`
	Effective             RetrievalParams         `json:"effective"`
	Sources               map[string]string       `json:"sources"`
}

type ReadOnlyEmbedding struct {
	Source    string `json:"source"`
	Model     string `json:"model"`
	Dimension int    `json:"dimension"`
}

type ReadOnlyChunking struct {
	Size         int    `json:"size"`
	Overlap      int    `json:"overlap"`
	SplitMarkers string `json:"splitMarkers"`
	MinSize      int    `json:"minSize"`
}

type ReadOnlyChat struct {
	Source string `json:"source"`
	Model  string `json:"model"`
}

type ReadOnlyRerank struct {
	Source string `json:"source"`
	Model  string `json:"model"`
}

type ReadOnlyParser struct {
	Version          string   `json:"version"`
	SupportedFormats []string `json:"supportedFormats"`
}

type ReadOnlySystemConfiguration struct {
	Embedding ReadOnlyEmbedding `json:"embedding"`
	Chunking  ReadOnlyChunking  `json:"chunking"`
	Chat      ReadOnlyChat      `json:"chat"`
	Rerank    ReadOnlyRerank    `json:"rerank"`
	Parser    ReadOnlyParser    `json:"parser"`
}

type KnowledgeBaseConfiguration struct {
	SchemaVersion    string                      `json:"schemaVersion"`
	KnowledgeBaseID  string                      `json:"knowledgeBaseId"`
	ACLVersion       int64                       `json:"aclVersion"`
	IndexingStrategy IndexingStrategy            `json:"indexingStrategy"`
	Retrieval        RetrievalConfiguration      `json:"retrieval"`
	ReadOnly         ReadOnlySystemConfiguration `json:"readOnly"`
}

type Document struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenantId"`
	KnowledgeBaseID string     `json:"knowledgeBaseId"`
	OwnerID         string     `json:"ownerId"`
	Filename        string     `json:"filename"`
	MimeType        string     `json:"mimeType"`
	Status          string     `json:"status"`
	LatestVersionID string     `json:"latestVersionId"`
	DeletedAt       *time.Time `json:"deletedAt,omitempty"`
	DeletedBy       string     `json:"deletedBy,omitempty"`
	PurgeAfter      *time.Time `json:"purgeAfter,omitempty"`
	RestoredAt      *time.Time `json:"restoredAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type DocumentVersion struct {
	ID              string    `json:"id"`
	DocumentID      string    `json:"documentId"`
	TenantID        string    `json:"tenantId"`
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	Version         int       `json:"version"`
	ContentHash     string    `json:"contentHash"`
	SizeBytes       int64     `json:"sizeBytes"`
	Status          string    `json:"status"`
	ACLVersion      int64     `json:"aclVersion"`
	ErrorCode       string    `json:"errorCode"`
	ErrorMessage    string    `json:"errorMessage"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type PipelineRun struct {
	ID                string    `json:"id"`
	PipelineType      string    `json:"pipelineType"`
	TenantID          string    `json:"tenantId"`
	KnowledgeBaseID   string    `json:"knowledgeBaseId"`
	DocumentID        string    `json:"documentId"`
	DocumentVersionID string    `json:"documentVersionId"`
	RequestID         string    `json:"requestId"`
	TraceID           string    `json:"traceId"`
	IdempotencyKey    string    `json:"idempotencyKey"`
	Status            string    `json:"status"`
	ErrorCode         string    `json:"errorCode"`
	ErrorMessage      string    `json:"errorMessage"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type StageRun struct {
	ID             uint64     `json:"id"`
	PipelineRunID  string     `json:"pipelineRunId"`
	Stage          string     `json:"stage"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	IdempotencyKey string     `json:"idempotencyKey"`
	ErrorCode      string     `json:"errorCode"`
	ErrorMessage   string     `json:"errorMessage"`
	StartedAt      *time.Time `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type DocumentView struct {
	Document Document        `json:"document"`
	Version  DocumentVersion `json:"version"`
	Pipeline PipelineRun     `json:"pipeline"`
	Stages   []StageRun      `json:"stages"`
}

type DocumentVersionSummary struct {
	ID           string `json:"id"`
	Version      int    `json:"version"`
	SizeBytes    int64  `json:"sizeBytes"`
	Status       string `json:"status"`
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
}

type DocumentSummary struct {
	Document     Document               `json:"document"`
	Version      DocumentVersionSummary `json:"version"`
	ContentReady bool                   `json:"contentReady"`
	Previewable  bool                   `json:"previewable"`
}

type DocumentPage struct {
	SchemaVersion   string            `json:"schemaVersion"`
	KnowledgeBaseID string            `json:"knowledgeBaseId"`
	Items           []DocumentSummary `json:"items"`
	NextCursor      string            `json:"nextCursor"`
	HasMore         bool              `json:"hasMore"`
}

type Chunk struct {
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

type ChunkPage struct {
	SchemaVersion string  `json:"schemaVersion"`
	DocumentID    string  `json:"documentId"`
	VersionID     string  `json:"versionId"`
	Status        string  `json:"status"`
	Items         []Chunk `json:"items"`
	NextCursor    int     `json:"nextCursor"`
	HasMore       bool    `json:"hasMore"`
}

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

type DocumentStatus struct {
	Document     Document        `json:"document"`
	Version      DocumentVersion `json:"version"`
	Pipeline     PipelineRun     `json:"pipeline"`
	Stages       []StageRun      `json:"stages"`
	ContentReady bool            `json:"contentReady"`
	Previewable  bool            `json:"previewable"`
}

type DocumentVersionView struct {
	ID           string    `json:"id"`
	Version      int       `json:"version"`
	ContentHash  string    `json:"contentHash"`
	SizeBytes    int64     `json:"sizeBytes"`
	Status       string    `json:"status"`
	ErrorCode    string    `json:"errorCode"`
	ErrorMessage string    `json:"errorMessage"`
	Current      bool      `json:"current"`
	ContentReady bool      `json:"contentReady"`
	Restorable   bool      `json:"restorable"`
	CreatedAt    time.Time `json:"createdAt"`
}

type DocumentVersionPage struct {
	SchemaVersion string                `json:"schemaVersion"`
	DocumentID    string                `json:"documentId"`
	CurrentID     string                `json:"currentVersionId"`
	Items         []DocumentVersionView `json:"items"`
}

type DocumentEnvelope struct {
	Document Document `json:"document" oa:"required"`
}

type DocumentUploadRequest struct {
	File       string `json:"file" binding:"required" oa:"binary"`
	DocumentID string `json:"document_id"`
}

// QueryPlanParams is the resolved parameter block embedded in a query plan.
// The retrieval domain intentionally has no JSON tags on Params, so the wire
// names are exported Go field names; this contract preserves that shape.
type QueryPlanParams struct {
	TopK                 int     `json:"TopK"`
	EmbeddingTopK        int     `json:"EmbeddingTopK"`
	KeywordTopK          int     `json:"KeywordTopK"`
	VectorThreshold      float64 `json:"VectorThreshold"`
	KeywordThreshold     float64 `json:"KeywordThreshold"`
	RerankEnabled        bool    `json:"RerankEnabled"`
	RerankTopK           int     `json:"RerankTopK"`
	RerankThreshold      float64 `json:"RerankThreshold"`
	VectorEFSearch       int     `json:"VectorEFSearch"`
	VectorOversample     int     `json:"VectorOversample"`
	VectorIterativeScan  bool    `json:"VectorIterativeScan"`
	ContextTokenLimit    int     `json:"ContextTokenLimit"`
	AdjacentExpansion    int     `json:"AdjacentExpansion"`
	MaxAdjacentChunks    int     `json:"MaxAdjacentChunks"`
	RRFK                 int     `json:"RRFK"`
	ExpansionScoreFactor float64 `json:"ExpansionScoreFactor"`
}

type QueryPlan struct {
	SchemaVersion             string          `json:"schemaVersion"`
	RequestID                 string          `json:"requestId"`
	TenantID                  string          `json:"tenantId"`
	KnowledgeBaseIDs          []string        `json:"knowledgeBaseIds"`
	AuthorizationScopeVersion string          `json:"authorizationScopeVersion"`
	Intent                    string          `json:"intent"`
	Mode                      string          `json:"mode"`
	Rewrite                   string          `json:"rewrite"`
	TopK                      int             `json:"topK"`
	VectorEnabled             bool            `json:"vectorEnabled"`
	KeywordEnabled            bool            `json:"keywordEnabled"`
	RerankEnabled             bool            `json:"rerankEnabled"`
	Channels                  []string        `json:"channels,omitempty"`
	Fusion                    string          `json:"fusion,omitempty"`
	RequiredCapabilities      []string        `json:"requiredCapabilities"`
	Params                    QueryPlanParams `json:"params"`
	HistoryTurns              int             `json:"historyTurns,omitempty"`
}

type EvidenceCandidate struct {
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

type EvidenceSet struct {
	SchemaVersion             string              `json:"schemaVersion"`
	RequestID                 string              `json:"requestId"`
	AuthorizationScopeVersion string              `json:"authorizationScopeVersion"`
	Query                     string              `json:"query"`
	Plan                      QueryPlan           `json:"plan"`
	Items                     []EvidenceCandidate `json:"items"`
	TokenEstimate             int                 `json:"tokenEstimate"`
	RecallLatencyMS           int64               `json:"recallLatencyMs"`
	RerankApplied             bool                `json:"rerankApplied"`
	DroppedByAuthorization    int                 `json:"droppedByAuthorization"`
}

type SearchRequest struct {
	Query           string `json:"query"`
	KnowledgeBaseID string `json:"knowledgeBaseId"`
	SessionOverride string `json:"sessionOverride"`
}

type ConversationCreateRequest struct {
	KnowledgeBaseID string `json:"knowledge_base_id" oa:"required"`
	Title           string `json:"title"`
}

type MessageCreateRequest struct {
	Query           string `json:"query" binding:"required"`
	RequestID       string `json:"requestId"`
	SessionOverride string `json:"sessionOverride"`
}

type Conversation struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenantId"`
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	UserID          string    `json:"userId"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ConversationList struct {
	Items []Conversation `json:"items"`
}

type Message struct {
	ID                        string    `json:"id"`
	ConversationID            string    `json:"conversationId"`
	TenantID                  string    `json:"tenantId"`
	KnowledgeBaseID           string    `json:"knowledgeBaseId"`
	UserID                    string    `json:"userId"`
	RequestID                 string    `json:"requestId"`
	Role                      string    `json:"role"`
	Content                   string    `json:"content"`
	Status                    string    `json:"status"`
	AuthorizationScopeVersion string    `json:"authorizationScopeVersion"`
	Model                     string    `json:"model"`
	LatencyMS                 int64     `json:"latencyMs"`
	ErrorCode                 string    `json:"errorCode,omitempty"`
	ErrorMessage              string    `json:"errorMessage,omitempty"`
	CreatedAt                 time.Time `json:"createdAt"`
	UpdatedAt                 time.Time `json:"updatedAt"`
}

type Citation struct {
	Index             int     `json:"index"`
	ChunkID           string  `json:"chunkId"`
	DocumentID        string  `json:"documentId"`
	DocumentVersionID string  `json:"documentVersionId"`
	KnowledgeBaseID   string  `json:"knowledgeBaseId"`
	HeadingPath       string  `json:"headingPath"`
	PageNumber        int     `json:"pageNumber"`
	SourcePath        string  `json:"sourcePath"`
	Quote             string  `json:"quote"`
	Rank              int     `json:"rank"`
	Score             float64 `json:"score"`
}

type MessageView struct {
	Message   Message    `json:"message"`
	Citations []Citation `json:"citations"`
}

type MessageList struct {
	Items []MessageView `json:"items"`
}

type StreamEvent struct {
	EventID   string         `json:"event_id"`
	RequestID string         `json:"request_id" oa:"required"`
	Sequence  int            `json:"sequence" oa:"required"`
	Type      string         `json:"type" oa:"required"`
	Data      map[string]any `json:"data,omitempty"`
}

type ConnectorConfig struct {
	ManifestURL       string  `json:"manifestUrl"`
	RequestsPerSecond float64 `json:"requestsPerSecond,omitempty"`
	MaxItemsPerRun    int     `json:"maxItemsPerRun"`
	ACLMode           string  `json:"aclMode"`
}

type CredentialInput struct {
	Type     string `json:"type"`
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type ConnectorCreateRequest struct {
	Name                string           `json:"name" oa:"required"`
	Type                string           `json:"type" oa:"required"`
	Config              ConnectorConfig  `json:"config"`
	Credential          *CredentialInput `json:"credential"`
	SyncIntervalSeconds int              `json:"syncIntervalSeconds"`
	Enabled             *bool            `json:"enabled"`
}

type ConnectorUpdateRequest struct {
	Name                *string          `json:"name"`
	Config              *ConnectorConfig `json:"config"`
	Credential          *CredentialInput `json:"credential"`
	SyncIntervalSeconds *int             `json:"syncIntervalSeconds"`
	Enabled             *bool            `json:"enabled"`
}

type Connector struct {
	ID                  string          `json:"id"`
	TenantID            string          `json:"tenantId"`
	KnowledgeBaseID     string          `json:"knowledgeBaseId"`
	Name                string          `json:"name"`
	Type                string          `json:"type"`
	Config              ConnectorConfig `json:"config"`
	Enabled             bool            `json:"enabled"`
	SyncIntervalSeconds int             `json:"syncIntervalSeconds"`
	NextSyncAt          *time.Time      `json:"nextSyncAt,omitempty"`
	LastSyncAt          *time.Time      `json:"lastSyncAt,omitempty"`
	HasCredential       bool            `json:"hasCredential"`
	CreatedBy           string          `json:"createdBy"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type ConnectorList struct {
	Connectors []Connector `json:"connectors" oa:"required"`
}

type ConnectorRun struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenantId"`
	ConnectorID     string          `json:"connectorId"`
	KnowledgeBaseID string          `json:"knowledgeBaseId"`
	Mode            string          `json:"mode"`
	Status          string          `json:"status"`
	Cursor          json.RawMessage `json:"cursor"`
	ResumeOfRunID   string          `json:"resumeOfRunId,omitempty"`
	TriggeredBy     string          `json:"triggeredBy"`
	ItemsTotal      int             `json:"itemsTotal"`
	ItemsCreated    int             `json:"itemsCreated"`
	ItemsUpdated    int             `json:"itemsUpdated"`
	ItemsDeleted    int             `json:"itemsDeleted"`
	ItemsSkipped    int             `json:"itemsSkipped"`
	ItemsFailed     int             `json:"itemsFailed"`
	ErrorSummary    string          `json:"errorSummary,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	Attempts        int             `json:"attempts"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
}

type ConnectorRunList struct {
	Runs []ConnectorRun `json:"runs" oa:"required"`
}

type PermissionMember struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

type ConnectorPermissions struct {
	ConnectorID     string             `json:"connectorId"`
	KnowledgeBaseID string             `json:"knowledgeBaseId"`
	SourceACL       string             `json:"sourceAcl"`
	EffectiveACL    string             `json:"effectiveAcl"`
	ACLMode         string             `json:"aclMode"`
	ACLVersion      int64              `json:"aclVersion"`
	Members         []PermissionMember `json:"members"`
}

type CancelResult struct {
	Cancelled bool `json:"cancelled" oa:"required"`
}

type StartResult struct {
	Started int `json:"started" oa:"required"`
}

type ProjectionRun struct {
	ID                string     `json:"id"`
	TenantID          string     `json:"tenantId"`
	KnowledgeBaseID   string     `json:"knowledgeBaseId"`
	EvidencePackageID string     `json:"evidencePackageId"`
	ProjectionType    string     `json:"projectionType"`
	Status            string     `json:"status"`
	SourceHash        string     `json:"sourceHash"`
	ACLVersion        int64      `json:"aclVersion"`
	Model             string     `json:"model"`
	PromptVersion     string     `json:"promptVersion"`
	RetryOfRunID      *string    `json:"retryOfRunId,omitempty"`
	ErrorCode         string     `json:"errorCode,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type ProjectionStageRun struct {
	ID              uint64     `json:"id"`
	ProjectionRunID string     `json:"projectionRunId"`
	ProjectionType  string     `json:"projectionType"`
	Stage           string     `json:"stage"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type PageCounts struct {
	Total         int64 `json:"total"`
	Published     int64 `json:"published"`
	PendingReview int64 `json:"pendingReview"`
	Stale         int64 `json:"stale"`
	Indexed       int64 `json:"indexed"`
}

type GraphCounts struct {
	Snapshots      int64  `json:"snapshots"`
	ActiveSnapshot string `json:"activeSnapshot,omitempty"`
	Entities       int64  `json:"entities"`
	Relations      int64  `json:"relations"`
	PendingReview  int64  `json:"pendingReview"`
}

type ProjectionState struct {
	KnowledgeBaseID string          `json:"knowledgeBaseId"`
	JoinStatus      string          `json:"joinStatus"`
	WikiEnabled     bool            `json:"wikiEnabled"`
	GraphEnabled    bool            `json:"graphEnabled"`
	Runs            []ProjectionRun `json:"runs"`
	RelationRuns    []ProjectionRun `json:"relationRuns"`
	Pages           PageCounts      `json:"pages"`
	Graph           GraphCounts     `json:"graph"`
}

type WikiPage struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenantId"`
	KnowledgeBaseID    string    `json:"knowledgeBaseId"`
	DocumentID         *string   `json:"documentId,omitempty"`
	PageType           string    `json:"pageType"`
	Slug               string    `json:"slug"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	CurrentVersionID   *string   `json:"currentVersionId,omitempty"`
	PublishedVersionID *string   `json:"publishedVersionId,omitempty"`
	ACLVersion         int64     `json:"aclVersion"`
	SourceHash         string    `json:"sourceHash"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type WikiPageVersion struct {
	ID              string     `json:"id"`
	WikiPageID      string     `json:"wikiPageId"`
	TenantID        string     `json:"tenantId"`
	KnowledgeBaseID string     `json:"knowledgeBaseId"`
	Version         int        `json:"version"`
	Content         string     `json:"content"`
	SourceHash      string     `json:"sourceHash"`
	GenerationModel string     `json:"generationModel"`
	PromptVersion   string     `json:"promptVersion"`
	ReviewStatus    string     `json:"reviewStatus"`
	ReviewNote      string     `json:"reviewNote,omitempty"`
	ReviewedBy      string     `json:"reviewedBy,omitempty"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
	ACLVersion      int64      `json:"aclVersion"`
	CreatedBy       string     `json:"createdBy,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type WikiBlock struct {
	HeadingPath    string   `json:"headingPath"`
	Content        string   `json:"content"`
	SourceChunkIDs []string `json:"sourceChunkIds"`
}

type WikiPageSource struct {
	TenantID          string `json:"tenantId"`
	KnowledgeBaseID   string `json:"knowledgeBaseId"`
	WikiPageVersionID string `json:"wikiPageVersionId"`
	DocumentID        string `json:"documentId"`
	DocumentVersionID string `json:"documentVersionId"`
	ChunkID           string `json:"chunkId"`
	SourcePath        string `json:"sourcePath"`
	HeadingPath       string `json:"headingPath"`
	PageNumber        int    `json:"pageNumber"`
}

type WikiPageSummary struct {
	Page             WikiPage         `json:"page"`
	CurrentVersion   *WikiPageVersion `json:"currentVersion,omitempty"`
	PublishedVersion *WikiPageVersion `json:"publishedVersion,omitempty"`
}

type WikiPageList struct {
	Pages []WikiPageSummary `json:"pages" oa:"required"`
}

type WikiPageDetail struct {
	Page    WikiPage         `json:"page"`
	Version *WikiPageVersion `json:"version,omitempty"`
	Blocks  []WikiBlock      `json:"blocks"`
	Sources []WikiPageSource `json:"sources"`
}

type WikiVersionList struct {
	Versions []WikiPageVersion `json:"versions" oa:"required"`
}

type WikiRunDetail struct {
	Run    ProjectionRun        `json:"run"`
	Stages []ProjectionStageRun `json:"stages"`
}

type WikiRunList struct {
	Runs []ProjectionRun `json:"runs" oa:"required"`
}

type WikiReviewRequest struct {
	Action string `json:"action" binding:"required"`
	Note   string `json:"note"`
}

type WikiRollbackRequest struct {
	VersionID string `json:"version_id" binding:"required"`
}

type KnowledgeHit struct {
	PageID          string           `json:"pageId"`
	VersionID       string           `json:"wikiPageVersionId"`
	KnowledgeBaseID string           `json:"knowledgeBaseId"`
	Title           string           `json:"title"`
	Content         string           `json:"content"`
	Score           float64          `json:"score"`
	Sources         []WikiPageSource `json:"sources"`
}

type KnowledgeSearchResult struct {
	Query  string         `json:"query"`
	Items  []KnowledgeHit `json:"items"`
	Joined bool           `json:"joined"`
}

type GraphSource struct {
	DocumentID        string `json:"documentId"`
	DocumentVersionID string `json:"documentVersionId"`
	ChunkID           string `json:"chunkId"`
	SourcePath        string `json:"sourcePath"`
	HeadingPath       string `json:"headingPath,omitempty"`
	PageNumber        int    `json:"pageNumber,omitempty"`
}

type GraphEntity struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Aliases    []string          `json:"aliases"`
	Sources    []GraphSource     `json:"sources"`
}

type GraphRelation struct {
	ID         string            `json:"id"`
	SubjectID  string            `json:"subjectId"`
	Subject    string            `json:"subject"`
	Predicate  string            `json:"predicate"`
	ObjectID   string            `json:"objectId"`
	Object     string            `json:"object"`
	Properties map[string]string `json:"properties,omitempty"`
	Confidence float64           `json:"confidence"`
	Sources    []GraphSource     `json:"sources"`
}

type GraphNode struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Aliases    []string          `json:"aliases,omitempty"`
	Sources    []GraphSource     `json:"sources,omitempty"`
	Depth      int               `json:"depth"`
}

type GraphPath struct {
	NodeIDs []string `json:"nodeIds"`
	EdgeIDs []string `json:"edgeIds"`
}

type GraphSnapshot struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenantId"`
	KnowledgeBaseID string     `json:"knowledgeBaseId"`
	ProjectionRunID string     `json:"projectionRunId"`
	Version         int        `json:"version"`
	Status          string     `json:"status"`
	SourceHash      string     `json:"sourceHash"`
	ACLVersion      int64      `json:"aclVersion"`
	Model           string     `json:"model"`
	PromptVersion   string     `json:"promptVersion"`
	Stats           string     `json:"stats"`
	ReviewNote      string     `json:"reviewNote,omitempty"`
	ReviewedBy      string     `json:"reviewedBy,omitempty"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
	ActivatedAt     *time.Time `json:"activatedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type GraphEntityAlias struct {
	TenantID        string    `json:"tenantId"`
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	EntityID        string    `json:"entityId"`
	Alias           string    `json:"alias"`
	Normalized      string    `json:"normalized"`
	Rule            string    `json:"rule"`
	MergedKey       string    `json:"mergedKey,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

type GraphSnapshotDetail struct {
	Snapshot  GraphSnapshot      `json:"snapshot"`
	Entities  []GraphEntity      `json:"entities"`
	Relations []GraphRelation    `json:"relations"`
	Aliases   []GraphEntityAlias `json:"aliases"`
}

type GraphSnapshotList struct {
	Snapshots []GraphSnapshot `json:"snapshots" oa:"required"`
}

type GraphQueryResult struct {
	Query      string          `json:"query"`
	SnapshotID string          `json:"snapshotId,omitempty"`
	Depth      int             `json:"depth"`
	Nodes      []GraphNode     `json:"nodes"`
	Edges      []GraphRelation `json:"edges"`
	Paths      []GraphPath     `json:"paths"`
	Truncated  bool            `json:"truncated"`
	Joined     bool            `json:"joined"`
}

type GraphRunDetail struct {
	Run      ProjectionRun        `json:"run"`
	Stages   []ProjectionStageRun `json:"stages"`
	Snapshot *GraphSnapshot       `json:"snapshot,omitempty"`
}

type GraphRunList struct {
	Runs []ProjectionRun `json:"runs" oa:"required"`
}

type GraphReviewRequest struct {
	Action string `json:"action" binding:"required"`
	Note   string `json:"note"`
}

type TaskDeadLetter struct {
	ID                uint64     `json:"id"`
	IdempotencyKey    string     `json:"idempotencyKey"`
	TenantID          string     `json:"tenantId"`
	KnowledgeBaseID   string     `json:"knowledgeBaseId"`
	DocumentVersionID string     `json:"documentVersionId"`
	TaskType          string     `json:"taskType"`
	Stage             string     `json:"stage"`
	Attempts          int        `json:"attempts"`
	LastError         string     `json:"lastError"`
	Status            string     `json:"status"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
	ResolvedBy        string     `json:"resolvedBy,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type DeadLetterList struct {
	Items []TaskDeadLetter `json:"items" oa:"required"`
}

type TaskInbox struct {
	IdempotencyKey string     `json:"idempotencyKey"`
	TaskType       string     `json:"taskType"`
	Status         string     `json:"status"`
	StageRunID     uint64     `json:"stageRunId"`
	TenantID       *string    `json:"tenantId,omitempty"`
	LeaseOwner     string     `json:"leaseOwner"`
	LeaseUntil     *time.Time `json:"leaseUntil"`
	ProcessedAt    *time.Time `json:"processedAt"`
	LastError      string     `json:"lastError"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type OutboxEvent struct {
	ID             uint64     `json:"id"`
	TenantID       string     `json:"tenantId"`
	AggregateType  string     `json:"aggregateType"`
	AggregateID    string     `json:"aggregateId"`
	EventType      string     `json:"eventType"`
	IdempotencyKey string     `json:"idempotencyKey"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	PublishedAt    *time.Time `json:"publishedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

type MechanismState struct {
	Mechanism     string     `json:"mechanism"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts,omitempty"`
	LeaseUntil    *time.Time `json:"leaseUntil,omitempty"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	Detail        string     `json:"detail,omitempty"`
}

type TaskInspection struct {
	Task       any              `json:"task" oa:"required"`
	Kind       string           `json:"kind" oa:"required"`
	Mechanisms []MechanismState `json:"mechanisms" oa:"required"`
	Inbox      *TaskInbox       `json:"inbox,omitempty"`
	Pipeline   *PipelineRun     `json:"pipeline,omitempty"`
	Stage      *StageRun        `json:"stage,omitempty"`
	Outbox     []OutboxEvent    `json:"outbox" oa:"required"`
	DeadLetter *TaskDeadLetter  `json:"deadLetter,omitempty"`
}

type ReconcileReport struct {
	DocumentsRepaired   int64 `json:"documentsRepaired"`
	VersionsRepaired    int64 `json:"versionsRepaired"`
	StagesRequeued      int   `json:"stagesRequeued"`
	PipelinesCancelled  int   `json:"pipelinesCancelled"`
	InboxesExpired      int64 `json:"inboxesExpired"`
	DeadLettersResolved int64 `json:"deadLettersResolved"`
}

type OpsMetrics map[string]any
