package models

import (
	"time"

	"github.com/hzc19970630/rag-platform/internal/status"
)

const (
	GraphSnapshotBuilding   = status.Building
	GraphSnapshotPending    = status.PendingReview
	GraphSnapshotActive     = status.Active
	GraphSnapshotRejected   = status.ReviewRejected
	GraphSnapshotStale      = status.Stale
	GraphSnapshotSuperseded = status.Superseded
	GraphSnapshotFailed     = status.Failed
)

// GraphSnapshot is the reviewable and publishable unit of the Relation Store.
// Only an ACTIVE snapshot whose ACL version still matches the knowledge base is
// visible to queries.
type GraphSnapshot struct {
	ID              string     `gorm:"primaryKey;size:64" json:"id"`
	TenantID        string     `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string     `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	ProjectionRunID string     `gorm:"size:64;not null;uniqueIndex" json:"projectionRunId"`
	Version         int        `gorm:"not null" json:"version"`
	Status          string     `gorm:"size:24;not null" json:"status"`
	SourceHash      string     `gorm:"size:64;not null;default:''" json:"sourceHash"`
	ACLVersion      int64      `gorm:"not null" json:"aclVersion"`
	Model           string     `gorm:"size:128;not null;default:''" json:"model"`
	PromptVersion   string     `gorm:"size:128;not null;default:''" json:"promptVersion"`
	Stats           string     `gorm:"type:jsonb;not null;default:'{}'" json:"stats"`
	ReviewNote      string     `gorm:"type:text;not null;default:''" json:"reviewNote,omitempty"`
	ReviewedBy      string     `gorm:"size:64;not null;default:''" json:"reviewedBy,omitempty"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
	ActivatedAt     *time.Time `json:"activatedAt,omitempty"`
	CreatedAt       time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updatedAt"`
}

func (GraphSnapshot) TableName() string { return "graph_snapshots" }

// GraphEntity is one normalized node. canonical_key is the merge key; aliases
// keep the original surface forms and the rule that produced them.
type GraphEntity struct {
	ID              string    `gorm:"primaryKey;size:64" json:"id"`
	TenantID        string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	SnapshotID      string    `gorm:"size:64;not null;index" json:"snapshotId"`
	CanonicalKey    string    `gorm:"size:512;not null" json:"canonicalKey"`
	Name            string    `gorm:"size:512;not null" json:"name"`
	EntityType      string    `gorm:"size:64;not null;default:''" json:"entityType"`
	Properties      string    `gorm:"type:jsonb;not null;default:'{}'" json:"properties"`
	ACLVersion      int64     `gorm:"not null" json:"aclVersion"`
	Status          string    `gorm:"size:16;not null;default:'ACTIVE'" json:"status"`
	CreatedAt       time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"not null" json:"updatedAt"`
}

func (GraphEntity) TableName() string { return "graph_entities" }

type GraphEntityAlias struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"-"`
	TenantID        string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	EntityID        string    `gorm:"size:64;not null;index" json:"entityId"`
	Alias           string    `gorm:"size:512;not null" json:"alias"`
	Normalized      string    `gorm:"size:512;not null" json:"normalized"`
	Rule            string    `gorm:"size:64;not null" json:"rule"`
	MergedKey       string    `gorm:"size:512;not null;default:''" json:"mergedKey,omitempty"`
	CreatedAt       time.Time `gorm:"not null" json:"createdAt"`
}

func (GraphEntityAlias) TableName() string { return "graph_entity_aliases" }

type GraphRelation struct {
	ID              string    `gorm:"primaryKey;size:64" json:"id"`
	TenantID        string    `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID string    `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	SnapshotID      string    `gorm:"size:64;not null;index" json:"snapshotId"`
	SubjectID       string    `gorm:"size:64;not null;index" json:"subjectId"`
	Predicate       string    `gorm:"size:256;not null" json:"predicate"`
	ObjectID        string    `gorm:"size:64;not null;index" json:"objectId"`
	Properties      string    `gorm:"type:jsonb;not null;default:'{}'" json:"properties"`
	Confidence      float64   `gorm:"not null;default:1" json:"confidence"`
	ACLVersion      int64     `gorm:"not null" json:"aclVersion"`
	Status          string    `gorm:"size:16;not null;default:'ACTIVE'" json:"status"`
	CreatedAt       time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"not null" json:"updatedAt"`
}

func (GraphRelation) TableName() string { return "graph_relations" }

// GraphRelationSource is the traceability record for one relation. A relation
// without at least one of these rows cannot enter a usable snapshot.
type GraphRelationSource struct {
	ID                uint64 `gorm:"primaryKey;autoIncrement" json:"-"`
	TenantID          string `gorm:"size:64;not null;index" json:"tenantId"`
	KnowledgeBaseID   string `gorm:"size:64;not null;index" json:"knowledgeBaseId"`
	RelationID        string `gorm:"size:64;not null;index" json:"relationId"`
	DocumentID        string `gorm:"size:64;not null" json:"documentId"`
	DocumentVersionID string `gorm:"size:64;not null" json:"documentVersionId"`
	ChunkID           string `gorm:"size:96;not null;index" json:"chunkId"`
	SourcePath        string `gorm:"size:512;not null;default:''" json:"sourcePath"`
	HeadingPath       string `gorm:"type:text;not null;default:''" json:"headingPath"`
	PageNumber        int    `gorm:"not null;default:0" json:"pageNumber"`
}

func (GraphRelationSource) TableName() string { return "graph_relation_sources" }
