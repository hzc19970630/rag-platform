package models

import "time"

// ProjectionGenerationCache is the per-package generation cache shared by Wiki
// and Relation projection. There is at most one row per (tenant, knowledge
// base, projection type, artifact, package); a prompt/model/ACL change
// overwrites the InputHash and payload instead of keeping every historical
// variant.
type ProjectionGenerationCache struct {
	TenantID        string    `gorm:"primaryKey;size:64" json:"tenantId"`
	KnowledgeBaseID string    `gorm:"primaryKey;size:64" json:"knowledgeBaseId"`
	ProjectionType  string    `gorm:"primaryKey;size:16" json:"projectionType"`
	Artifact        string    `gorm:"primaryKey;size:32" json:"artifact"`
	PackageID       string    `gorm:"primaryKey;size:64" json:"packageId"`
	InputHash       string    `gorm:"size:64;not null" json:"inputHash"`
	Payload         string    `gorm:"type:jsonb;not null" json:"-"`
	PromptVersion   string    `gorm:"size:128;not null;default:''" json:"promptVersion"`
	Model           string    `gorm:"size:128;not null;default:''" json:"model"`
	ACLVersion      int64     `gorm:"not null" json:"aclVersion"`
	UpdatedAt       time.Time `gorm:"not null" json:"updatedAt"`
}

func (ProjectionGenerationCache) TableName() string { return "projection_generation_cache" }
