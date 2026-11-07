package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testTask struct {
	N int `json:"n"`
}

func testPublisher(t *testing.T, batchSize int, publish func(context.Context, testTask) error) (*Publisher, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.OutboxEvent{}); err != nil {
		t.Fatalf("migrate outbox: %v", err)
	}
	publisher := New(Options{
		DB:          db,
		MaxAttempts: 3,
		BatchSize:   batchSize,
		Lease:       time.Minute,
		Label:       "test publisher",
		Decode: func(payload []byte) (any, error) {
			var task testTask
			if err := json.Unmarshal(payload, &task); err != nil {
				return nil, err
			}
			return task, nil
		},
		Publish: func(ctx context.Context, _ models.OutboxEvent, task any) error {
			return publish(ctx, task.(testTask))
		},
	})
	return publisher, db
}

func seedEvents(t *testing.T, db *gorm.DB, payloads ...string) {
	t.Helper()
	for index, payload := range payloads {
		event := models.OutboxEvent{
			TenantID:       "tenant-1",
			AggregateType:  "document_version",
			AggregateID:    fmt.Sprintf("version-%d", index),
			EventType:      "document.parse",
			IdempotencyKey: fmt.Sprintf("event-%d", index),
			Payload:        payload,
		}
		if err := db.Create(&event).Error; err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
}

func unpublishedCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.OutboxEvent{}).Where("published_at IS NULL").Count(&count).Error; err != nil {
		t.Fatalf("count unpublished: %v", err)
	}
	return count
}

func TestPublishBatchClaimsOnlyBatchSizeAndPendingDrainsRest(t *testing.T) {
	var published []int
	publisher, db := testPublisher(t, 2, func(_ context.Context, task testTask) error {
		published = append(published, task.N)
		return nil
	})
	seedEvents(t, db, `{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`)

	count, err := publisher.publishBatch(context.Background())
	if err != nil {
		t.Fatalf("publish batch: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 published in one batch, got %d", count)
	}
	if remaining := unpublishedCount(t, db); remaining != 3 {
		t.Fatalf("expected 3 unpublished after first batch, got %d", remaining)
	}

	if err := publisher.PublishPending(context.Background()); err != nil {
		t.Fatalf("drain pending: %v", err)
	}
	if remaining := unpublishedCount(t, db); remaining != 0 {
		t.Fatalf("expected all events published, %d remain", remaining)
	}
	if len(published) != 5 {
		t.Fatalf("expected 5 publish calls, got %d", len(published))
	}
}

func TestPublishFailureSetsBackoffAndAttempts(t *testing.T) {
	publisher, db := testPublisher(t, 4, func(_ context.Context, _ testTask) error {
		return errors.New("redis unavailable")
	})
	seedEvents(t, db, `{"n":1}`)

	count, err := publisher.publishBatch(context.Background())
	if err != nil {
		t.Fatalf("publish batch: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no published events, got %d", count)
	}
	var event models.OutboxEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if event.Attempts != 1 {
		t.Fatalf("expected attempts 1, got %d", event.Attempts)
	}
	if event.PublishedAt != nil {
		t.Fatal("failed event must not be marked published")
	}
	if event.NextAttemptAt == nil || !event.NextAttemptAt.After(time.Now()) {
		t.Fatalf("expected a future backoff, got %+v", event.NextAttemptAt)
	}
}

func TestMalformedPayloadParksAtAttemptLimit(t *testing.T) {
	publisher, db := testPublisher(t, 4, func(_ context.Context, _ testTask) error {
		t.Fatal("malformed payload must not be published")
		return nil
	})
	seedEvents(t, db, `{not-json`)

	count, err := publisher.publishBatch(context.Background())
	if err != nil {
		t.Fatalf("publish batch: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no published events, got %d", count)
	}
	var event models.OutboxEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatalf("load event: %v", err)
	}
	if event.Attempts != publisher.options.MaxAttempts {
		t.Fatalf("expected attempts %d, got %d", publisher.options.MaxAttempts, event.Attempts)
	}
}
