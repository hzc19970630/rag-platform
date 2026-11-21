package models

import "time"

// TableAsset is the parsed schema and coverage of one table block. Raw rows
// remain in chunks; this row is the retrieval representation used to decide
// what is vectorized and to support structured filtering.
type TableAsset struct {
	ID                string    `gorm:"primaryKey;size:64" json:"id"`
	TenantID          string    `gorm:"size:64;not null;index"`
	KnowledgeBaseID   string    `gorm:"size:64;not null;index"`
	DocumentID        string    `gorm:"size:64;not null;index"`
	DocumentVersionID string    `gorm:"size:64;not null;index"`
	Sheet             string    `gorm:"size:255;not null;default:''" json:"sheet"`
	SourcePath        string    `gorm:"size:512;not null;default:''" json:"sourcePath"`
	Header            string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	Columns           string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	RowStart          int       `gorm:"not null;default:0" json:"rowStart"`
	RowEnd            int       `gorm:"not null;default:0" json:"rowEnd"`
	RowCount          int       `gorm:"not null;default:0" json:"rowCount"`
	ColumnCount       int       `gorm:"not null;default:0" json:"columnCount"`
	SampleRows        string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	Stats             string    `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	IndexMode         string    `gorm:"size:16;not null;default:''" json:"indexMode"`
	CreatedAt         time.Time `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time `gorm:"not null" json:"updatedAt"`
}

func (TableAsset) TableName() string { return "table_assets" }
