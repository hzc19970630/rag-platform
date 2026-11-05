package ingestion

import (
	"errors"
	"time"
)

const SchemaDocumentVersionPage = "DocumentVersionPageV1"

var errReparseReused = errors.New("reparse target already exists")

// DocumentVersionView is one immutable version in the history. It never
// exposes the source or normalized object keys, because those stay server side
// like the current-version projections in preview.go.
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
