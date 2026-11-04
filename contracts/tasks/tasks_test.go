package tasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/contracts/evidence"
)

func envelope() evidence.Envelope {
	return evidence.Envelope{
		SchemaVersion: "IngestionTaskV1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-a", DocumentVersionID: "ver-a", PipelineRunID: "run-a", StageRunID: "7",
		TraceID: "trace-a", RequestID: "req-a", ActorID: "user-a", IdempotencyKey: "key-a",
		ContentHash: "hash-a", CreatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
	}
}

func TestIngestionJSONContract(t *testing.T) {
	task := Ingestion{Envelope: envelope(), TaskType: TypeIngestion, Stage: "parse", ObjectKey: "tenant/doc/source", PackageID: "pkg-a", Queue: "interactive"}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"schema_version": "IngestionTaskV1", "tenant_id": "tenant-a", "knowledge_base_id": "kb-a",
		"document_id": "doc-a", "document_version_id": "ver-a", "pipeline_run_id": "run-a",
		"stage_run_id": "7", "trace_id": "trace-a", "request_id": "req-a", "actor_id": "user-a",
		"idempotency_key": "key-a", "content_hash": "hash-a", "created_at": "2026-11-04T10:00:00Z",
		"task_type": TypeIngestion, "stage": "parse", "object_key": "tenant/doc/source",
		"package_id": "pkg-a", "queue": "interactive",
	}
	for key, expected := range want {
		if got := decoded[key]; got != expected {
			t.Fatalf("field %s = %v, want %q", key, got, expected)
		}
	}
}

func TestSyncJSONContract(t *testing.T) {
	task := Sync{
		SchemaVersion: SchemaSync, TenantID: "tenant-a", ConnectorID: "conn-a", SyncRunID: "run-a",
		IdempotencyKey: "key-a", Queue: "maintenance",
		CreatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"schemaVersion": SchemaSync, "tenantId": "tenant-a", "connectorId": "conn-a",
		"syncRunId": "run-a", "idempotencyKey": "key-a", "queue": "maintenance",
		"createdAt": "2026-11-04T10:00:00Z",
	}
	for key, expected := range want {
		if got := decoded[key]; got != expected {
			t.Fatalf("field %s = %v, want %q", key, got, expected)
		}
	}
}

func TestProjectionJSONContractAndQueueID(t *testing.T) {
	task := Projection{
		SchemaVersion: SchemaWiki, TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		EvidencePackageID: "pkg-a", ProjectionRunID: "run-a", ProjectionType: "wiki",
		Stage: "GENERATE", IdempotencyKey: "key-a", Attempt: 2,
	}
	if got := task.QueueID(); got != "key-a:2" {
		t.Fatalf("QueueID = %q", got)
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"schema_version": SchemaWiki, "tenant_id": "tenant-a", "knowledge_base_id": "kb-a",
		"evidence_package_id": "pkg-a", "projection_run_id": "run-a", "projection_type": "wiki",
		"stage": "GENERATE", "idempotency_key": "key-a",
	}
	for key, expected := range want {
		if got := decoded[key]; got != expected {
			t.Fatalf("field %s = %v, want %q", key, got, expected)
		}
	}
	if got := decoded["attempt"]; got != float64(2) {
		t.Fatalf("attempt = %v", got)
	}
}
