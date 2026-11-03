// Package evidence owns the published EvidencePackage contract shared by the
// ingestion pipeline and projections. Moving it here keeps Pipeline B from
// depending on ingestion internals.
package evidence

import (
	"context"
	"time"
)

const Schema = "EvidencePackageV1"

type Envelope struct {
	SchemaVersion     string    `json:"schema_version"`
	TenantID          string    `json:"tenant_id"`
	KnowledgeBaseID   string    `json:"knowledge_base_id"`
	DocumentID        string    `json:"document_id"`
	DocumentVersionID string    `json:"document_version_id"`
	PipelineRunID     string    `json:"pipeline_run_id"`
	StageRunID        string    `json:"stage_run_id"`
	TraceID           string    `json:"trace_id"`
	RequestID         string    `json:"request_id"`
	ActorID           string    `json:"actor_id"`
	IdempotencyKey    string    `json:"idempotency_key"`
	ContentHash       string    `json:"content_hash"`
	CreatedAt         time.Time `json:"created_at"`
}

type Chunk struct {
	ID                 string `json:"id"`
	ParentID           string `json:"parent_id"`
	Ordinal            int    `json:"ordinal"`
	Content            string `json:"content"`
	ContentHash        string `json:"content_hash"`
	HeadingPath        string `json:"heading_path"`
	PageNumber         int    `json:"page_number"`
	SourcePath         string `json:"source_path"`
	StartOffset        int    `json:"start_offset"`
	EndOffset          int    `json:"end_offset"`
	EmbeddingDimension int    `json:"embedding_dimension"`
}

type Package struct {
	SchemaVersion string `json:"schema_version"`
	Envelope
	// ID is the stored evidence_packages row id. It is intentionally not part of
	// the serialized package: existing package JSON predates the field, and the
	// database row remains the authority for identity.
	ID          string  `json:"-"`
	ContentHash string  `json:"content_hash"`
	ACLVersion  int64   `json:"acl_version"`
	Status      string  `json:"status"`
	Chunks      []Chunk `json:"chunks"`
}

// Reader is the projection-facing port. The PostgreSQL adapter lives in
// platform/evidencestore; another store can implement the same contract.
type Reader interface {
	CurrentPackages(ctx context.Context, tenantID, knowledgeBaseID string) ([]Package, error)
}
