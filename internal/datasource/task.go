package datasource

import "github.com/example/rag-platform/contracts/tasks"

const (
	// TaskTypeSync is the Asynq task type for one connector sync attempt. It is
	// intentionally separate from document.ingestion.v1: a sync converts remote
	// objects into document versions, while ingestion parses those versions.
	TaskTypeSync = tasks.TypeSync
	SchemaTask   = tasks.SchemaSync

	QueueMaintenance = "maintenance"
)

// SyncTask is the durable hand-off between the outbox and the worker. It names
// a run that already exists in PostgreSQL, so a lost or duplicated queue
// message can never start work that the run history does not describe.
type SyncTask = tasks.Sync
