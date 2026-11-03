package datasource

import "time"

const (
	// TaskTypeSync is the Asynq task type for one connector sync attempt. It is
	// intentionally separate from document.ingestion.v1: a sync converts remote
	// objects into document versions, while ingestion parses those versions.
	TaskTypeSync = "connector.sync.v1"
	SchemaTask   = "connector.sync.v1"

	QueueMaintenance = "maintenance"
)

// SyncTask is the durable hand-off between the outbox and the worker. It names
// a run that already exists in PostgreSQL, so a lost or duplicated queue
// message can never start work that the run history does not describe.
type SyncTask struct {
	SchemaVersion  string    `json:"schemaVersion"`
	TenantID       string    `json:"tenantId"`
	ConnectorID    string    `json:"connectorId"`
	SyncRunID      string    `json:"syncRunId"`
	IdempotencyKey string    `json:"idempotencyKey"`
	Queue          string    `json:"queue"`
	CreatedAt      time.Time `json:"createdAt"`
}
