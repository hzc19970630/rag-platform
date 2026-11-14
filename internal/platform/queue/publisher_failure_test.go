package queue

import (
	"context"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/hzc19970630/rag-platform/contracts/tasks"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
)

func deadPublisher() *Publisher {
	options := asynq.RedisClientOpt{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		PoolSize:     1,
	}
	return &Publisher{client: asynq.NewClient(options), inspector: asynq.NewInspector(options)}
}

func TestPublisherSurfacesRedisFailures(t *testing.T) {
	publisher := deadPublisher()
	defer publisher.Close()
	ctx := context.Background()
	projection := tasks.Projection{IdempotencyKey: "projection:1", Attempt: 1}

	failures := map[string]error{
		"ingestion": publisher.Publish(ctx, tasks.Ingestion{Envelope: evidence.Envelope{IdempotencyKey: "ingestion:1"}}),
		"sync":      publisher.PublishSync(ctx, tasks.Sync{IdempotencyKey: "sync:1"}),
		"wiki":      publisher.PublishWiki(ctx, projection),
		"relation":  publisher.PublishRelation(ctx, projection),
		"answer":    publisher.PublishAnswer(ctx, tasks.Answer{JobID: "answer:1"}),
		"migration": publisher.EnqueueEmbeddingMigration(ctx, "tenant-1", "migration-1", 1),
	}
	for name, err := range failures {
		if err == nil {
			t.Fatalf("%s publish must fail without Redis", name)
		}
	}

	if err := publisher.RemoveArchived(ctx, tasks.Ingestion{}); err != nil {
		t.Fatalf("empty idempotency key must be a no-op: %v", err)
	}
	if err := publisher.RemoveArchived(ctx, tasks.Ingestion{Envelope: evidence.Envelope{IdempotencyKey: "ingestion:1"}}); err == nil {
		t.Fatal("archived removal must surface the Redis failure")
	}
	if _, err := publisher.QueueDepth(ctx); err == nil {
		t.Fatal("queue depth must surface the Redis failure")
	}
}
