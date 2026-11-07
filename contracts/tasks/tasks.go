// Package tasks owns the versioned payloads exchanged between the outbox,
// the queue adapter and workers. Transport code depends on this package instead
// of importing domain packages, so the queue layer cannot drift with a domain
// refactor.
package tasks

import (
	"strconv"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
)

const (
	TypeIngestion = "document.ingestion.v1"
	TypeSync      = "connector.sync.v1"
	TypeWiki      = "knowledge.projection.wiki.v1"
	TypeRelation  = "knowledge.projection.relation.v1"
	TypeAnswer    = "answer.generate.v1"

	SchemaIngestion = "IngestionTaskV1"
	SchemaSync      = "connector.sync.v1"
	SchemaWiki      = "WikiProjectionTaskV1"
	SchemaRelation  = "RelationProjectionTaskV1"
	SchemaAnswer    = "AnswerTaskV1"
)

// Ingestion is one Evidence pipeline stage. It carries ids and an object key,
// never document content.
type Ingestion struct {
	evidence.Envelope
	TaskType  string `json:"task_type"`
	Stage     string `json:"stage"`
	ObjectKey string `json:"object_key,omitempty"`
	PackageID string `json:"package_id,omitempty"`
	// Queue selects the worker pool. Empty means the ingestion pool; operator
	// retries use the interactive pool so they are not stuck behind bulk work.
	Queue string `json:"queue,omitempty"`
}

// Sync names a connector sync run that already exists in PostgreSQL, so a lost
// or duplicated message can never start work the run history does not describe.
type Sync struct {
	SchemaVersion  string    `json:"schemaVersion"`
	TenantID       string    `json:"tenantId"`
	ConnectorID    string    `json:"connectorId"`
	SyncRunID      string    `json:"syncRunId"`
	IdempotencyKey string    `json:"idempotencyKey"`
	Queue          string    `json:"queue"`
	CreatedAt      time.Time `json:"createdAt"`
}

// Projection is one Wiki or relation projection stage. It carries ids and a
// stage, never page content or object keys.
type Projection struct {
	SchemaVersion     string `json:"schema_version"`
	TenantID          string `json:"tenant_id"`
	KnowledgeBaseID   string `json:"knowledge_base_id"`
	EvidencePackageID string `json:"evidence_package_id"`
	ProjectionRunID   string `json:"projection_run_id"`
	ProjectionType    string `json:"projection_type"`
	Stage             string `json:"stage"`
	IdempotencyKey    string `json:"idempotency_key"`
	// Attempt distinguishes queue task ids across independent retries while the
	// stage row keeps the same idempotency key.
	Attempt int `json:"attempt"`
}

func (t Projection) QueueID() string {
	return t.IdempotencyKey + ":" + strconv.Itoa(t.Attempt)
}

// Answer asks a worker to generate one recorded question. It carries the
// durable job id and identity ids only: credentials, the principal and the
// authorization scope stay in PostgreSQL (answer_jobs.scope_json), so a queue
// message cannot be replayed into a wider scope. JobID doubles as the Asynq
// task id, which makes redelivery idempotent without colliding on a
// client-supplied request id.
type Answer struct {
	SchemaVersion   string `json:"schema_version"`
	JobID           string `json:"job_id"`
	TenantID        string `json:"tenant_id"`
	SubjectID       string `json:"subject_id"`
	ConversationID  string `json:"conversation_id"`
	RequestID       string `json:"request_id"`
	SessionOverride string `json:"session_override,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
}
