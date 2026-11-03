package models

import "time"

// ProjectionRun is one Pipeline B execution over an immutable EvidencePackage.
// It never owns Pipeline A state: retries and rebuilds change only these rows.
type ProjectionRun struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID          string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	EvidencePackageID string     `gorm:"size:64;not null;index" json:"evidencePackageId"`
	ProjectionType    string     `gorm:"size:16;not null" json:"projectionType"`
	Status            string     `gorm:"size:32;not null" json:"status"`
	SourceHash        string     `gorm:"size:64;not null;default:''" json:"sourceHash"`
	ACLVersion        int64      `gorm:"not null" json:"aclVersion"`
	Model             string     `gorm:"size:128;not null;default:''" json:"model"`
	PromptVersion     string     `gorm:"size:128;not null;default:''" json:"promptVersion"`
	RetryOfRunID      *string    `gorm:"size:64" json:"retryOfRunId,omitempty"`
	ErrorCode         string     `gorm:"size:128;not null;default:''" json:"errorCode,omitempty"`
	ErrorMessage      string     `gorm:"type:text;not null;default:''" json:"errorMessage,omitempty"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	CreatedAt         time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updatedAt"`
}

func (ProjectionRun) TableName() string { return "projection_runs" }

type ProjectionStageRun struct {
	ID              uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ProjectionRunID string     `gorm:"size:64;not null;index" json:"projectionRunId"`
	ProjectionType  string     `gorm:"size:16;not null" json:"projectionType"`
	Stage           string     `gorm:"size:32;not null" json:"stage"`
	Status          string     `gorm:"size:32;not null" json:"status"`
	Attempts        int        `gorm:"not null;default:0" json:"attempts"`
	IdempotencyKey  string     `gorm:"size:255;not null;uniqueIndex" json:"idempotencyKey"`
	ErrorCode       string     `gorm:"size:128;not null;default:''" json:"errorCode,omitempty"`
	ErrorMessage    string     `gorm:"type:text;not null;default:''" json:"errorMessage,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	CreatedAt       time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updatedAt"`
}

func (ProjectionStageRun) TableName() string { return "projection_stage_runs" }

// WikiPage is the logical page. published_version_id is what readers see; a
// pending newer version sets REVIEW_REQUIRED but does not remove the published
// version from the Knowledge Index.
type WikiPage struct {
	ID                 string    `gorm:"primaryKey;size:64" json:"id"`
	TenantID           string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID    string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	DocumentID         *string   `gorm:"size:64" json:"documentId,omitempty"`
	PageType           string    `gorm:"size:16;not null" json:"pageType"`
	Slug               string    `gorm:"size:255;not null" json:"slug"`
	Title              string    `gorm:"size:512;not null" json:"title"`
	Status             string    `gorm:"size:32;not null" json:"status"`
	CurrentVersionID   *string   `gorm:"size:64" json:"currentVersionId,omitempty"`
	PublishedVersionID *string   `gorm:"size:64" json:"publishedVersionId,omitempty"`
	ACLVersion         int64     `gorm:"not null" json:"aclVersion"`
	SourceHash         string    `gorm:"size:64;not null;default:''" json:"sourceHash"`
	CreatedAt          time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt          time.Time `gorm:"not null" json:"updatedAt"`
}

func (WikiPage) TableName() string { return "wiki_pages" }

// WikiBlock is one traceable content block. Every block must name at least one
// EvidencePackage chunk; a block without sources is rejected at generation.
type WikiBlock struct {
	HeadingPath    string   `json:"headingPath"`
	Content        string   `json:"content"`
	SourceChunkIDs []string `json:"sourceChunkIds"`
}

type WikiPageVersion struct {
	ID              string     `gorm:"primaryKey;size:64" json:"id"`
	WikiPageID      string     `gorm:"size:64;not null;index" json:"wikiPageId"`
	TenantID        string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	Version         int        `gorm:"not null" json:"version"`
	Content         string     `gorm:"type:text;not null;default:''" json:"content"`
	Blocks          string     `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	SourceHash      string     `gorm:"size:64;not null" json:"sourceHash"`
	GenerationModel string     `gorm:"size:128;not null;default:''" json:"generationModel"`
	PromptVersion   string     `gorm:"size:128;not null;default:''" json:"promptVersion"`
	ReviewStatus    string     `gorm:"size:32;not null" json:"reviewStatus"`
	ReviewNote      string     `gorm:"type:text;not null;default:''" json:"reviewNote,omitempty"`
	ReviewedBy      string     `gorm:"size:64;not null;default:''" json:"reviewedBy,omitempty"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
	ACLVersion      int64      `gorm:"not null" json:"aclVersion"`
	CreatedBy       string     `gorm:"size:64;not null;default:''" json:"createdBy,omitempty"`
	CreatedAt       time.Time  `gorm:"not null" json:"createdAt"`
}

func (WikiPageVersion) TableName() string { return "wiki_page_versions" }

type WikiPageSource struct {
	ID                uint64 `gorm:"primaryKey;autoIncrement" json:"-"`
	TenantID          string `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	WikiPageVersionID string `gorm:"size:64;not null;index" json:"wikiPageVersionId"`
	DocumentID        string `gorm:"size:64;not null" json:"documentId"`
	DocumentVersionID string `gorm:"size:64;not null" json:"documentVersionId"`
	ChunkID           string `gorm:"size:96;not null;index" json:"chunkId"`
	SourcePath        string `gorm:"size:512;not null;default:''" json:"sourcePath"`
	HeadingPath       string `gorm:"type:text;not null;default:''" json:"headingPath"`
	PageNumber        int    `gorm:"not null;default:0" json:"pageNumber"`
}

func (WikiPageSource) TableName() string { return "wiki_page_sources" }

// KnowledgeIndexRecord is the page-level index entry. Only the published,
// ACL-current version of a PUBLISHED page has an ACTIVE row.
type KnowledgeIndexRecord struct {
	ID                 string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID           string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID    string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	WikiPageID         string     `gorm:"size:64;not null;index" json:"wikiPageId"`
	WikiPageVersionID  string     `gorm:"size:64;not null" json:"wikiPageVersionId"`
	Title              string     `gorm:"size:512;not null" json:"title"`
	Content            string     `gorm:"type:text;not null" json:"content"`
	ContentHash        string     `gorm:"size:64;not null" json:"contentHash"`
	Embedding          string     `gorm:"type:halfvec" json:"-"`
	EmbeddingModel     string     `gorm:"size:128;not null;default:''" json:"embeddingModel"`
	EmbeddingDimension int        `gorm:"not null;default:0" json:"embeddingDimension"`
	ACLVersion         int64      `gorm:"not null" json:"aclVersion"`
	Status             string     `gorm:"size:16;not null;default:'ACTIVE'" json:"status"`
	IndexedAt          *time.Time `json:"indexedAt,omitempty"`
	CreatedAt          time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt          time.Time  `gorm:"not null" json:"updatedAt"`
}

func (KnowledgeIndexRecord) TableName() string { return "knowledge_index_records" }
