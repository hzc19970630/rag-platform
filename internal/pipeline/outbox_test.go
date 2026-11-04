package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
)

func TestRequeueEventCreatesMissingEvent(t *testing.T) {
	db := newTestDB(t)
	queued, err := RequeueEvent(db, models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{}`,
	})
	if err != nil || !queued {
		t.Fatalf("requeue missing event = %v, %v", queued, err)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", "run:parse").Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if event.PublishedAt != nil || event.Attempts != 0 {
		t.Fatalf("created event = %+v", event)
	}
}

func TestRequeueEventLeavesUnpublishedEventAlone(t *testing.T) {
	db := newTestDB(t)
	if err := db.Create(&models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{"attempt":"first"}`,
	}).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	queued, err := RequeueEvent(db, models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{"attempt":"second"}`,
	})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if queued {
		t.Fatal("an unpublished event already has a pending delivery")
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", "run:parse").Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if event.Payload != `{"attempt":"first"}` {
		t.Fatalf("queued event payload changed to %q", event.Payload)
	}
}

func TestRequeueEventReopensPublishedEvent(t *testing.T) {
	db := newTestDB(t)
	published := time.Now()
	if err := db.Create(&models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{}`,
		Attempts: 4, PublishedAt: &published, LastError: "boom",
	}).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	queued, err := RequeueEvent(db, models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{}`,
	})
	if err != nil || !queued {
		t.Fatalf("requeue published event = %v, %v", queued, err)
	}
	var event models.OutboxEvent
	if err := db.First(&event, "idempotency_key = ?", "run:parse").Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if event.PublishedAt != nil || event.Attempts != 0 || event.LastError != "" {
		t.Fatalf("reopened event = %+v", event)
	}
}

func TestReopenEventRejectsOtherTenant(t *testing.T) {
	db := newTestDB(t)
	if err := db.Create(&models.OutboxEvent{
		TenantID: "tenant-a", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{}`,
	}).Error; err != nil {
		t.Fatalf("seed event: %v", err)
	}
	err := ReopenEvent(db, models.OutboxEvent{
		TenantID: "tenant-b", AggregateType: "pipeline_run", AggregateID: "run-1",
		EventType: "document.parse", IdempotencyKey: "run:parse", Payload: `{}`,
	})
	if err == nil {
		t.Fatal("reopening another tenant's event must fail")
	}
}

func TestUpsertDeadLetterLifecycle(t *testing.T) {
	db := newTestDB(t)
	row := models.TaskDeadLetter{
		IdempotencyKey: "run:parse", TenantID: "tenant-a", KnowledgeBaseID: "kb-1",
		DocumentVersionID: "ver-1", TaskType: "ingestion", Stage: "parse",
		Attempts: 5, LastError: "first", Payload: `{"stage":"parse"}`,
	}
	if err := UpsertDeadLetter(db, row); err != nil {
		t.Fatalf("create dead letter: %v", err)
	}
	var letter models.TaskDeadLetter
	if err := db.First(&letter, "idempotency_key = ?", "run:parse").Error; err != nil {
		t.Fatalf("load dead letter: %v", err)
	}
	if letter.Status != DeadLetterPending || letter.Attempts != 5 || letter.LastError != "first" {
		t.Fatalf("created dead letter = %+v", letter)
	}
	row.LastError = "second"
	if err := UpsertDeadLetter(db, row); err != nil {
		t.Fatalf("update dead letter: %v", err)
	}
	if err := db.First(&letter, "idempotency_key = ?", "run:parse").Error; err != nil {
		t.Fatalf("reload dead letter: %v", err)
	}
	if letter.LastError != "second" || letter.Status != DeadLetterPending {
		t.Fatalf("updated dead letter = %+v", letter)
	}
	if err := db.Model(&letter).Update("status", DeadLetterResolved).Error; err != nil {
		t.Fatalf("resolve dead letter: %v", err)
	}
	row.LastError = "third"
	if err := UpsertDeadLetter(db, row); err != nil {
		t.Fatalf("upsert resolved dead letter: %v", err)
	}
	if err := db.First(&letter, "idempotency_key = ?", "run:parse").Error; err != nil {
		t.Fatalf("reload resolved dead letter: %v", err)
	}
	if letter.Status != DeadLetterResolved || letter.LastError != "second" {
		t.Fatalf("resolved dead letter must stay untouched, got %+v", letter)
	}
}

func TestFailProcessingInboxOnlyTouchesProcessing(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	for _, row := range []models.TaskInbox{
		{IdempotencyKey: "run:pending", TaskType: "ingestion", Status: InboxProcessing, CreatedAt: now, UpdatedAt: now},
		{IdempotencyKey: "run:done", TaskType: "ingestion", Status: InboxSucceeded, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed inbox: %v", err)
		}
	}
	if err := FailProcessingInbox(context.Background(), db, "run:pending", "retry budget exhausted"); err != nil {
		t.Fatalf("fail processing inbox: %v", err)
	}
	if err := FailProcessingInbox(context.Background(), db, "run:done", "stale failure"); err != nil {
		t.Fatalf("fail succeeded inbox: %v", err)
	}
	pending := loadInbox(t, db, "run:pending")
	if pending.Status != InboxFailed || pending.LastError != "retry budget exhausted" {
		t.Fatalf("processing inbox = %+v", pending)
	}
	done := loadInbox(t, db, "run:done")
	if done.Status != InboxSucceeded || done.LastError != "" {
		t.Fatalf("succeeded inbox must not be rewritten, got %+v", done)
	}
}
