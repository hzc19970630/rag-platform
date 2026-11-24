package models

import "time"

// TableAsset is the parsed schema and coverage of one table block. Raw rows
// remain in chunks; this row is the retrieval representation used to decide
// what is vectorized and to support structured filtering.
type TableAsset struct {
	ID                string `gorm:"primaryKey;size:64" json:"id"`
	TenantID          string `gorm:"size:64;not null;index"`
	KnowledgeBaseID   string `gorm:"size:64;not null;index"`
	DocumentID        string `gorm:"size:64;not null;index"`
	DocumentVersionID string `gorm:"size:64;not null;index"`
	Sheet             string `gorm:"size:255;not null;default:''" json:"sheet"`
	SourcePath        string `gorm:"size:512;not null;default:''" json:"sourcePath"`
	Header            string `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	Columns           string `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	RowStart          int    `gorm:"not null;default:0" json:"rowStart"`
	RowEnd            int    `gorm:"not null;default:0" json:"rowEnd"`
	RowCount          int    `gorm:"not null;default:0" json:"rowCount"`
	ColumnCount       int    `gorm:"not null;default:0" json:"columnCount"`
	SampleRows        string `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	Stats             string `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	IndexMode         string `gorm:"size:16;not null;default:''" json:"indexMode"`
	StorageMode       string `gorm:"size:16;not null;default:'full'" json:"storageMode"`
	ParseStatus       string `gorm:"size:16;not null;default:'parsed'" json:"parseStatus"`
	Truncated         bool   `gorm:"not null;default:false" json:"truncated"`
	TotalRows         int64  `gorm:"not null;default:0" json:"totalRows"`
	ParsedRows        int64  `gorm:"not null;default:0" json:"parsedRows"`
	HeaderRow         int    `gorm:"not null;default:0" json:"headerRow"`
	// B39 columnar copy metadata. P1 exposes these fields through the coverage
	// API and the document index-coverage card.
	StorageURI        string     `gorm:"size:1024;not null;default:''" json:"-"`
	StorageFormat     string     `gorm:"size:16;not null;default:''" json:"-"`
	SchemaJSON        string     `gorm:"type:jsonb;not null;default:'{}'" json:"-"`
	MaterializedRows  int64      `gorm:"not null;default:0" json:"-"`
	ByteSize          int64      `gorm:"not null;default:0" json:"-"`
	MaterializedAt    *time.Time `json:"-"`
	MaterializeStatus string     `gorm:"size:16;not null;default:''" json:"-"`
	MaterializeError  string     `gorm:"not null;default:''" json:"-"`
	CreatedAt         time.Time  `gorm:"not null" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updatedAt"`
}

func (TableAsset) TableName() string { return "table_assets" }
