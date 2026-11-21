package models

import "time"

// TableRow is one raw row of a table asset. Cells are keyed by column name so
// deterministic filters and TopN queries do not parse chunk text.
type TableRow struct {
	ID                string    `gorm:"primaryKey;size:64"`
	TenantID          string    `gorm:"size:64;not null;index"`
	KnowledgeBaseID   string    `gorm:"size:64;not null;index"`
	DocumentID        string    `gorm:"size:64;not null;index"`
	DocumentVersionID string    `gorm:"size:64;not null;index"`
	TableAssetID      string    `gorm:"size:64;not null;index"`
	RowNumber         int       `gorm:"not null" json:"rowNumber"`
	Cells             string    `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	CreatedAt         time.Time `gorm:"not null"`
}

func (TableRow) TableName() string { return "table_rows" }
