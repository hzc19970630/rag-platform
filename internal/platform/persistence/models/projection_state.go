package models

import "time"

// KnowledgeProjectionState caches the expected source hashes for a knowledge
// base's enabled projections. The hashes are derived from the current Evidence
// Packages plus ACL/prompt/model; materializing them here keeps ProjectionJoin
// gating to one row read instead of rescanning every package on each query.
type KnowledgeProjectionState struct {
	TenantID              string    `gorm:"primaryKey;size:64"`
	KnowledgeBaseID       string    `gorm:"primaryKey;size:64"`
	ACLVersion            int64     `gorm:"not null;default:0"`
	WikiExpectedHash      string    `gorm:"size:64;not null;default:''"`
	WikiPromptVersion     string    `gorm:"size:128;not null;default:''"`
	WikiModel             string    `gorm:"size:128;not null;default:''"`
	RelationExpectedHash  string    `gorm:"size:64;not null;default:''"`
	RelationPromptVersion string    `gorm:"size:128;not null;default:''"`
	RelationModel         string    `gorm:"size:128;not null;default:''"`
	UpdatedAt             time.Time `gorm:"not null"`
}

func (KnowledgeProjectionState) TableName() string { return "knowledge_projection_states" }
