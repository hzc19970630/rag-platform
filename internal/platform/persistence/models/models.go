package models

import "time"

type User struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	Email       string    `gorm:"size:255;not null" json:"email"`
	DisplayName string    `gorm:"size:255;not null" json:"displayName"`
	OIDCIssuer  string    `gorm:"column:oidc_issuer;size:512;not null;default:''" json:"-"`
	OIDCSubject string    `gorm:"column:oidc_subject;size:255;not null;default:''" json:"-"`
	CreatedAt   time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"not null" json:"updatedAt"`
}

func (User) TableName() string { return "users" }

type Tenant struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	Quota     string    `gorm:"type:jsonb;not null;default:'{}'" json:"quota"`
	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

func (Tenant) TableName() string { return "tenants" }

// AuthSession is the server-side half of a signed session token. The JWT alone
// is not enough to authenticate: logout and key rotation revoke the row, so a
// stolen token stops working immediately instead of when it expires.
type AuthSession struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	UserID     string     `gorm:"size:64;not null;index" json:"userId"`
	TenantID   string     `gorm:"size:64;not null;index" json:"tenantId"`
	TokenID    string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	ExpiresAt  time.Time  `gorm:"not null" json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	LastSeenAt time.Time  `gorm:"not null" json:"lastSeenAt"`
	UserAgent  string     `gorm:"size:512;not null;default:''" json:"userAgent"`
	IPAddress  string     `gorm:"size:128;not null;default:''" json:"ipAddress"`
	CreatedAt  time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt  time.Time  `gorm:"not null" json:"updatedAt"`
}

func (AuthSession) TableName() string { return "auth_sessions" }

// APIKey stores only the hash. The raw key is returned once at creation, and
// the prefix is a non-secret lookup handle so authentication does not scan every
// row.
type APIKey struct {
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID         string     `gorm:"size:64;not null;index" json:"tenantId"`
	CreatedBy        string     `gorm:"size:64;not null" json:"createdBy"`
	Name             string     `gorm:"size:255;not null" json:"name"`
	KeyPrefix        string     `gorm:"size:32;not null;uniqueIndex" json:"keyPrefix"`
	KeyHash          string     `gorm:"size:128;not null;uniqueIndex" json:"-"`
	Capabilities     string     `gorm:"type:jsonb;not null;default:'[]'" json:"capabilities"`
	KnowledgeBaseIDs string     `gorm:"type:jsonb;not null;default:'[]'" json:"knowledgeBaseIds"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt       *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt        time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updatedAt"`
}

func (APIKey) TableName() string { return "api_keys" }

type AuthorizationRevision struct {
	TenantID  string    `gorm:"primaryKey;size:64" json:"tenantId"`
	Revision  int64     `gorm:"not null;default:1" json:"revision"`
	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

func (AuthorizationRevision) TableName() string { return "authorization_revisions" }

type Membership struct {
	TenantID  string    `gorm:"primaryKey;size:64" json:"tenantId"`
	UserID    string    `gorm:"primaryKey;size:64" json:"userId"`
	Role      string    `gorm:"size:32;not null" json:"role"`
	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt time.Time `gorm:"not null" json:"updatedAt"`
}

func (Membership) TableName() string { return "memberships" }

type KnowledgeBase struct {
	ID          string `gorm:"primaryKey;size:64" json:"id"`
	TenantID    string `gorm:"size:64;not null;index" json:"tenantId"`
	OwnerID     string `gorm:"size:64;not null" json:"ownerId"`
	Name        string `gorm:"size:255;not null" json:"name"`
	Description string `gorm:"type:text;not null;default:''" json:"description"`
	// EmbeddingModel and EmbeddingDimension pin the vector space this knowledge
	// base is written and queried in. The binding is set at creation and cannot
	// be changed once documents exist; the managed rebuild moves both together.
	EmbeddingModel     string `gorm:"size:128;not null;default:''" json:"embeddingModel"`
	EmbeddingDimension int    `gorm:"not null;default:0" json:"embeddingDimension"`
	ACLVersion         int64  `gorm:"not null;default:1" json:"aclVersion"`
	// RetrievalConfig is the knowledge-base tier of the retrieval parameter
	// override chain (YAML defaults -> knowledge base -> session). Stored in the
	// database because it is tenant-scoped, runtime-changeable and auditable.
	RetrievalConfig string `gorm:"type:jsonb;not null;default:'{}'" json:"retrievalConfig"`
	// IndexingStrategy holds the vector/keyword/wiki/graph capability switches.
	IndexingStrategy string    `gorm:"type:jsonb;not null;default:'{}'" json:"indexingStrategy"`
	CreatedAt        time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt        time.Time `gorm:"not null" json:"updatedAt"`
}

func (KnowledgeBase) TableName() string { return "knowledge_bases" }

type KnowledgeBaseMember struct {
	KnowledgeBaseID string    `gorm:"primaryKey;size:64" json:"knowledgeBaseId"`
	UserID          string    `gorm:"primaryKey;size:64" json:"userId"`
	Role            string    `gorm:"size:32;not null" json:"role"`
	CreatedAt       time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"not null" json:"updatedAt"`
}

func (KnowledgeBaseMember) TableName() string { return "knowledge_base_members" }

type AuditLog struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID     string    `gorm:"size:64;not null;index" json:"tenantId"`
	ActorID      string    `gorm:"size:64;not null" json:"actorId"`
	Action       string    `gorm:"size:64;not null" json:"action"`
	ResourceType string    `gorm:"size:64;not null" json:"resourceType"`
	ResourceID   string    `gorm:"size:128;not null" json:"resourceId"`
	Allowed      bool      `gorm:"not null" json:"allowed"`
	Reason       string    `gorm:"type:text;not null;default:''" json:"reason"`
	RequestID    string    `gorm:"size:128;not null;default:''" json:"requestId"`
	IPAddress    string    `gorm:"size:128;not null;default:''" json:"ipAddress"`
	UserAgent    string    `gorm:"size:512;not null;default:''" json:"userAgent"`
	CreatedAt    time.Time `gorm:"not null" json:"createdAt"`
}

func (AuditLog) TableName() string { return "audit_logs" }

type Document struct {
	ID              string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID        string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	OwnerID         string     `gorm:"size:64;not null" json:"ownerId"`
	Filename        string     `gorm:"size:512;not null" json:"filename"`
	MimeType        string     `gorm:"size:128;not null" json:"mimeType"`
	Status          string     `gorm:"size:32;not null" json:"status"`
	LatestVersionID string     `gorm:"size:64" json:"latestVersionId"`
	DeletedAt       *time.Time `json:"deletedAt,omitempty"`
	DeletedBy       string     `gorm:"size:64;not null;default:''" json:"deletedBy,omitempty"`
	PurgeAfter      *time.Time `json:"purgeAfter,omitempty"`
	RestoredAt      *time.Time `json:"restoredAt,omitempty"`
	CreatedAt       time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updatedAt"`
}

func (Document) TableName() string { return "documents" }

// DocumentVersion is one immutable parse of a document. Its object storage
// locations stay server side: SourceObjectKey and NormalizedKey are deliberately
// not serialized, so API responses describe versions without handing out object
// addresses or credentials.
type DocumentVersion struct {
	ID              string    `gorm:"primaryKey;size:64" json:"id"`
	DocumentID      string    `gorm:"size:64;not null;index" json:"documentId"`
	TenantID        string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	Version         int       `gorm:"not null" json:"version"`
	ContentHash     string    `gorm:"size:64;not null" json:"contentHash"`
	SizeBytes       int64     `gorm:"not null" json:"sizeBytes"`
	SourceObjectKey string    `gorm:"size:1024;not null" json:"-"`
	NormalizedKey   string    `gorm:"size:1024" json:"-"`
	Status          string    `gorm:"size:32;not null" json:"status"`
	ACLVersion      int64     `gorm:"not null" json:"aclVersion"`
	ErrorCode       string    `gorm:"size:128" json:"errorCode"`
	ErrorMessage    string    `gorm:"type:text" json:"errorMessage"`
	CreatedAt       time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"not null" json:"updatedAt"`
}

func (DocumentVersion) TableName() string { return "document_versions" }

type PipelineRun struct {
	ID                string    `gorm:"primaryKey;size:64" json:"id"`
	PipelineType      string    `gorm:"size:32;not null" json:"pipelineType"`
	TenantID          string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	DocumentID        string    `gorm:"size:64;not null" json:"documentId"`
	DocumentVersionID string    `gorm:"size:64;not null;index" json:"documentVersionId"`
	RequestID         string    `gorm:"size:128;not null" json:"requestId"`
	TraceID           string    `gorm:"size:128;not null" json:"traceId"`
	IdempotencyKey    string    `gorm:"size:255;not null;uniqueIndex" json:"idempotencyKey"`
	Status            string    `gorm:"size:32;not null" json:"status"`
	ErrorCode         string    `gorm:"size:128" json:"errorCode"`
	ErrorMessage      string    `gorm:"type:text" json:"errorMessage"`
	CreatedAt         time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time `gorm:"not null" json:"updatedAt"`
}

func (PipelineRun) TableName() string { return "pipeline_runs" }

type StageRun struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PipelineRunID  string     `gorm:"size:64;not null;index" json:"pipelineRunId"`
	Stage          string     `gorm:"size:32;not null" json:"stage"`
	Status         string     `gorm:"size:32;not null" json:"status"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	IdempotencyKey string     `gorm:"size:255;not null;uniqueIndex" json:"idempotencyKey"`
	ErrorCode      string     `gorm:"size:128" json:"errorCode"`
	ErrorMessage   string     `gorm:"type:text" json:"errorMessage"`
	StartedAt      *time.Time `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt"`
	CreatedAt      time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updatedAt"`
}

func (StageRun) TableName() string { return "stage_runs" }

type Chunk struct {
	ID                 string    `gorm:"primaryKey;size:96" json:"id"`
	TenantID           string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID    string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	DocumentVersionID  string    `gorm:"size:64;not null;index" json:"documentVersionId"`
	ParentID           string    `gorm:"size:96" json:"parentId"`
	Ordinal            int       `gorm:"not null" json:"ordinal"`
	Content            string    `gorm:"type:text;not null" json:"content"`
	ContentHash        string    `gorm:"size:64;not null" json:"contentHash"`
	HeadingPath        string    `gorm:"type:text" json:"headingPath"`
	PageNumber         int       `json:"pageNumber"`
	SourcePath         string    `gorm:"size:512" json:"sourcePath"`
	StartOffset        int       `json:"startOffset"`
	EndOffset          int       `json:"endOffset"`
	Embedding          string    `gorm:"type:halfvec" json:"-"`
	EmbeddingDimension int       `gorm:"not null;default:0" json:"embeddingDimension"`
	Status             string    `gorm:"size:32;not null" json:"status"`
	CreatedAt          time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt          time.Time `gorm:"not null" json:"updatedAt"`
}

func (Chunk) TableName() string { return "chunks" }

type EvidencePackage struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID          string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	DocumentVersionID string     `gorm:"size:64;not null;uniqueIndex" json:"documentVersionId"`
	ContentHash       string     `gorm:"size:64;not null" json:"contentHash"`
	ACLVersion        int64      `gorm:"not null" json:"aclVersion"`
	Status            string     `gorm:"size:32;not null" json:"status"`
	PackageJSON       string     `gorm:"type:jsonb;not null" json:"-"`
	PublishedAt       *time.Time `json:"publishedAt"`
	CreatedAt         time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updatedAt"`
}

func (EvidencePackage) TableName() string { return "evidence_packages" }

type OutboxEvent struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID       string     `gorm:"size:64;not null;index" json:"tenantId"`
	AggregateType  string     `gorm:"size:64;not null" json:"aggregateType"`
	AggregateID    string     `gorm:"size:128;not null" json:"aggregateId"`
	EventType      string     `gorm:"size:128;not null" json:"eventType"`
	IdempotencyKey string     `gorm:"size:255;not null;uniqueIndex" json:"idempotencyKey"`
	Payload        string     `gorm:"type:jsonb;not null" json:"-"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	LastError      string     `gorm:"type:text;not null;default:''" json:"lastError,omitempty"`
	PublishedAt    *time.Time `json:"publishedAt"`
	CreatedAt      time.Time  `gorm:"not null" json:"createdAt"`
}

func (OutboxEvent) TableName() string { return "outbox_events" }

// TaskDeadLetter is an ingestion task whose retries were exhausted. It keeps
// the original payload so an operator can retry it without asking the user to
// re-upload, and a status so retry/discard decisions are auditable.
type TaskDeadLetter struct {
	ID                uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	IdempotencyKey    string     `gorm:"size:255;not null;uniqueIndex" json:"idempotencyKey"`
	TenantID          string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string     `gorm:"size:64;not null" json:"knowledgeBaseId"`
	DocumentVersionID string     `gorm:"size:64;not null" json:"documentVersionId"`
	TaskType          string     `gorm:"size:128;not null" json:"taskType"`
	Stage             string     `gorm:"size:32;not null" json:"stage"`
	Attempts          int        `gorm:"not null" json:"attempts"`
	LastError         string     `gorm:"type:text;not null" json:"lastError"`
	Payload           string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	Status            string     `gorm:"size:32;not null;default:'PENDING'" json:"status"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
	ResolvedBy        string     `gorm:"size:64;not null;default:''" json:"resolvedBy,omitempty"`
	CreatedAt         time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updatedAt"`
}

func (TaskDeadLetter) TableName() string { return "task_dead_letters" }

type TaskInbox struct {
	IdempotencyKey string     `gorm:"primaryKey;size:255" json:"idempotencyKey"`
	TaskType       string     `gorm:"size:128;not null" json:"taskType"`
	Status         string     `gorm:"size:32;not null" json:"status"`
	StageRunID     uint64     `json:"stageRunId"`
	ProcessedAt    *time.Time `json:"processedAt"`
	LastError      string     `gorm:"type:text" json:"lastError"`
	CreatedAt      time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updatedAt"`
}

func (TaskInbox) TableName() string { return "task_inbox" }

// Connector binds one external source to one knowledge base. CredentialRef is
// an AES-GCM envelope and is never serialized; config holds only non-secret
// settings. The connector does not own an index: every synced item becomes a
// regular DocumentVersion that the existing ingestion pipeline processes.
type Connector struct {
	ID                  string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID            string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID     string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	Name                string     `gorm:"size:255;not null" json:"name"`
	Type                string     `gorm:"size:64;not null" json:"type"`
	Config              string     `gorm:"type:jsonb;not null;default:'{}'" json:"config"`
	CredentialRef       string     `gorm:"column:credential_ref;type:text;not null;default:''" json:"-"`
	Enabled             bool       `gorm:"not null;default:true" json:"enabled"`
	SyncIntervalSeconds int        `gorm:"not null;default:0" json:"syncIntervalSeconds"`
	NextSyncAt          *time.Time `json:"nextSyncAt,omitempty"`
	LastSyncAt          *time.Time `json:"lastSyncAt,omitempty"`
	CreatedBy           string     `gorm:"size:64;not null" json:"createdBy"`
	CreatedAt           time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt           time.Time  `gorm:"not null" json:"updatedAt"`
}

func (Connector) TableName() string { return "connectors" }

// ConnectorItem maps one remote item to one document. ExternalID is the stable
// identity; DocumentID is derived from connector + external id so reruns update
// the same document instead of creating duplicates.
type ConnectorItem struct {
	ID                uint64     `gorm:"primaryKey;autoIncrement" json:"-"`
	TenantID          string     `gorm:"size:64;not null;index" json:"tenantId"`
	ConnectorID       string     `gorm:"size:64;not null;index" json:"connectorId"`
	ExternalID        string     `gorm:"size:512;not null" json:"externalId"`
	RemotePath        string     `gorm:"size:1024;not null;default:''" json:"remotePath"`
	RemoteVersion     string     `gorm:"size:255;not null;default:''" json:"remoteVersion"`
	ContentHash       string     `gorm:"size:64;not null;default:''" json:"contentHash"`
	DocumentID        *string    `gorm:"size:64" json:"documentId,omitempty"`
	DocumentVersionID *string    `gorm:"size:64" json:"documentVersionId,omitempty"`
	Status            string     `gorm:"size:32;not null;default:'ACTIVE'" json:"status"`
	LastSyncedAt      *time.Time `json:"lastSyncedAt,omitempty"`
	TombstonedAt      *time.Time `json:"tombstonedAt,omitempty"`
	CreatedAt         time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updatedAt"`
}

func (ConnectorItem) TableName() string { return "connector_items" }

type SyncRun struct {
	ID              string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID        string     `gorm:"size:64;not null;index" json:"tenantId"`
	ConnectorID     string     `gorm:"size:64;not null;index" json:"connectorId"`
	KnowledgeBaseID string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	Mode            string     `gorm:"size:16;not null" json:"mode"`
	Status          string     `gorm:"size:32;not null" json:"status"`
	Cursor          string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	ResumeOfRunID   *string    `gorm:"size:64" json:"resumeOfRunId,omitempty"`
	TriggeredBy     string     `gorm:"size:64;not null;default:''" json:"triggeredBy"`
	ItemsTotal      int        `gorm:"not null;default:0" json:"itemsTotal"`
	ItemsCreated    int        `gorm:"not null;default:0" json:"itemsCreated"`
	ItemsUpdated    int        `gorm:"not null;default:0" json:"itemsUpdated"`
	ItemsDeleted    int        `gorm:"not null;default:0" json:"itemsDeleted"`
	ItemsSkipped    int        `gorm:"not null;default:0" json:"itemsSkipped"`
	ItemsFailed     int        `gorm:"not null;default:0" json:"itemsFailed"`
	ErrorSummary    string     `gorm:"type:text;not null;default:''" json:"errorSummary,omitempty"`
	CancelRequested bool       `gorm:"not null;default:false" json:"cancelRequested"`
	Attempts        int        `gorm:"not null;default:0" json:"attempts"`
	LeaseUntil      *time.Time `json:"-"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updatedAt"`
}

func (SyncRun) TableName() string { return "sync_runs" }

type Conversation struct {
	ID              string    `gorm:"primaryKey;size:64" json:"id"`
	TenantID        string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	UserID          string    `gorm:"size:64;not null" json:"userId"`
	Title           string    `gorm:"size:255;not null;default:''" json:"title"`
	CreatedAt       time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"not null" json:"updatedAt"`
}

func (Conversation) TableName() string { return "conversations" }

type Message struct {
	ID              string `gorm:"primaryKey;size:64" json:"id"`
	ConversationID  string `gorm:"size:64;not null;index" json:"conversationId"`
	TenantID        string `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	UserID          string `gorm:"size:64;not null" json:"userId"`
	RequestID       string `gorm:"size:128;not null" json:"requestId"`
	Role            string `gorm:"size:16;not null" json:"role"`
	Content         string `gorm:"type:text;not null;default:''" json:"content"`
	Status          string `gorm:"size:32;not null" json:"status"`
	// Answer provenance: nullable because a user message has none of it, and an
	// empty string is not a valid jsonb literal.
	QueryPlan                 *string   `gorm:"type:jsonb" json:"-"`
	EvidenceSet               *string   `gorm:"type:jsonb" json:"-"`
	CitationValidation        *string   `gorm:"type:jsonb" json:"-"`
	AuthorizationScopeVersion string    `gorm:"size:128;not null;default:''" json:"authorizationScopeVersion"`
	Model                     string    `gorm:"size:128;not null;default:''" json:"model"`
	TokenUsage                *string   `gorm:"type:jsonb" json:"-"`
	LatencyMS                 int64     `gorm:"not null;default:0" json:"latencyMs"`
	ErrorCode                 string    `gorm:"size:128" json:"errorCode,omitempty"`
	ErrorMessage              string    `gorm:"type:text" json:"errorMessage,omitempty"`
	CreatedAt                 time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt                 time.Time `gorm:"not null" json:"updatedAt"`
}

func (Message) TableName() string { return "messages" }

type MessageCitation struct {
	ID                string    `gorm:"primaryKey;size:96" json:"id"`
	MessageID         string    `gorm:"size:64;not null;index" json:"messageId"`
	TenantID          string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	DocumentID        string    `gorm:"size:64;not null" json:"documentId"`
	DocumentVersionID string    `gorm:"size:64;not null" json:"documentVersionId"`
	ChunkID           string    `gorm:"size:96;not null" json:"chunkId"`
	CitationIndex     int       `gorm:"not null" json:"citationIndex"`
	Rank              int       `gorm:"not null" json:"rank"`
	Score             float64   `gorm:"not null;default:0" json:"score"`
	Quote             string    `gorm:"type:text;not null;default:''" json:"quote"`
	CreatedAt         time.Time `gorm:"not null" json:"createdAt"`
}

func (MessageCitation) TableName() string { return "message_citations" }

type StreamEvent struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"-"`
	RequestID string    `gorm:"size:128;not null;index" json:"requestId"`
	TenantID  string    `gorm:"size:64;not null;index" json:"tenantId"`
	UserID    string    `gorm:"size:64;not null;default:''" json:"userId"`
	Sequence  int       `gorm:"not null" json:"sequence"`
	EventType string    `gorm:"size:32;not null" json:"eventType"`
	Payload   string    `gorm:"type:jsonb;not null" json:"-"`
	CreatedAt time.Time `gorm:"not null" json:"createdAt"`
}

func (StreamEvent) TableName() string { return "stream_events" }
