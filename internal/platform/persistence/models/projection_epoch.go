package models

import "time"

// ProjectionChange is one ordered projection intent for a knowledge base.
// Evidence add/update/delete and ACL changes append one row and bump
// target_epoch; tombstones stay until the applied epoch covers them.
type ProjectionChange struct {
	ID                string `gorm:"primaryKey;size:64"`
	TenantID          string `gorm:"size:64;not null;index;uniqueIndex:projection_changes_idem"`
	KnowledgeBaseID   string `gorm:"size:64;not null;index;uniqueIndex:projection_changes_idem"`
	Kind              string `gorm:"size:16;not null"`
	DocumentID        string `gorm:"size:64;not null;default:''"`
	DocumentVersionID string `gorm:"size:64;not null;default:''"`
	EvidencePackageID string `gorm:"size:64;not null;default:''"`
	ContentHash       string `gorm:"size:64;not null;default:''"`
	Epoch             int64  `gorm:"not null"`
	IdempotencyKey    string `gorm:"size:255;not null;uniqueIndex:projection_changes_idem"`
	AppliedAt         *time.Time
	CreatedAt         time.Time `gorm:"not null"`
}

func (ProjectionChange) TableName() string { return "projection_changes" }

// WikiGeneration is one atomic knowledge-base generation. Exactly one row per
// knowledge base may be ACTIVE; readers resolve pages through it.
type WikiGeneration struct {
	ID              string    `gorm:"primaryKey;size:64"`
	TenantID        string    `gorm:"size:64;not null;index"`
	KnowledgeBaseID string    `gorm:"size:64;not null;index"`
	GenerationNo    int       `gorm:"not null"`
	Status          string    `gorm:"size:16;not null"`
	BaseGenerationID *string   `gorm:"size:64"`
	ProjectionRunID string    `gorm:"size:64;not null;default:''"`
	TargetEpoch     int64     `gorm:"not null;default:0"`
	SourceHash      string    `gorm:"size:64;not null;default:''"`
	CreatedAt       time.Time `gorm:"not null"`
	UpdatedAt       time.Time `gorm:"not null"`
}

func (WikiGeneration) TableName() string { return "wiki_generations" }

// WikiGenerationPage maps a generation to the page version it serves. CARRIED
// rows reuse the previous ACTIVE version; EXCEPTION rows are rejected versions
// whose predecessor is kept explicitly.
type WikiGenerationPage struct {
	TenantID          string    `gorm:"size:64;not null;index"`
	KnowledgeBaseID   string    `gorm:"size:64;not null;index"`
	GenerationID      string    `gorm:"primaryKey;size:64"`
	WikiPageID        string    `gorm:"primaryKey;size:64"`
	WikiPageVersionID *string   `gorm:"size:64"`
	Status            string    `gorm:"size:16;not null"`
	CreatedAt         time.Time `gorm:"not null"`
	UpdatedAt         time.Time `gorm:"not null"`
}

func (WikiGenerationPage) TableName() string { return "wiki_generation_pages" }
