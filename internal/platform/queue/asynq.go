package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/example/rag-platform/internal/datasource"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/knowledgeprojection"
	"github.com/hibiken/asynq"
)

const (
	TaskTypeIngestion = "document.ingestion.v1"
	// TaskTypeSync mirrors datasource.TaskTypeSync. The queue package owns the
	// transport name so the worker mux and the outbox publisher cannot drift.
	TaskTypeSync = "connector.sync.v1"
	// TaskTypeWiki carries one Wiki projection stage.
	TaskTypeWiki = "knowledge.projection.wiki.v1"
	// TaskTypeRelation carries one relation projection stage. It shares the
	// projection queue with Wiki but is an independent task type so one
	// pipeline's failures cannot be retried as the other.
	TaskTypeRelation = "knowledge.projection.relation.v1"

	QueueIngestion   = "ingestion"
	QueueInteractive = "interactive"
	QueueMaintenance = "maintenance"
	// QueueProjection isolates Pipeline B from ingestion and online work, so a
	// large upload backlog cannot delay Wiki generation or vice versa.
	QueueProjection = "projection"
)

type Publisher struct {
	client    *asynq.Client
	inspector *asynq.Inspector
}

func NewPublisher(redisAddr, redisPassword string) *Publisher {
	options := asynq.RedisClientOpt{Addr: redisAddr, Password: redisPassword}
	return &Publisher{client: asynq.NewClient(options), inspector: asynq.NewInspector(options)}
}

func (p *Publisher) Close() error {
	_ = p.inspector.Close()
	return p.client.Close()
}

func (p *Publisher) Publish(ctx context.Context, task ingestion.Task) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal ingestion task: %w", err)
	}
	_, err = p.client.EnqueueContext(ctx, asynq.NewTask(TaskTypeIngestion, payload), asynq.Queue(poolFor(task)), asynq.TaskID(task.IdempotencyKey), asynq.MaxRetry(5))
	if err != nil && err != asynq.ErrTaskIDConflict {
		return fmt.Errorf("enqueue ingestion task: %w", err)
	}
	return nil
}

// PublishSync enqueues one connector sync attempt. Sync work runs in the
// maintenance pool so a bulk upload backlog cannot delay an operator retry of a
// failed connector.
func (p *Publisher) PublishSync(ctx context.Context, task datasource.SyncTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal connector sync task: %w", err)
	}
	_, err = p.client.EnqueueContext(ctx, asynq.NewTask(TaskTypeSync, payload), asynq.Queue(QueueMaintenance), asynq.TaskID(task.IdempotencyKey), asynq.MaxRetry(5))
	if err != nil && err != asynq.ErrTaskIDConflict {
		return fmt.Errorf("enqueue connector sync task: %w", err)
	}
	return nil
}

// PublishWiki enqueues one Wiki projection stage in its own pool. The task id
// includes the attempt so an independent retry is not suppressed by Asynq's
// retained completed-task record.
func (p *Publisher) PublishWiki(ctx context.Context, task knowledgeprojection.Task) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal wiki projection task: %w", err)
	}
	_, err = p.client.EnqueueContext(ctx, asynq.NewTask(TaskTypeWiki, payload), asynq.Queue(QueueProjection), asynq.TaskID(task.QueueID()), asynq.MaxRetry(5))
	if err != nil && err != asynq.ErrTaskIDConflict {
		return fmt.Errorf("enqueue wiki projection task: %w", err)
	}
	return nil
}

// PublishRelation enqueues one relation projection stage. It uses a distinct
// Asynq task type from Wiki so retries and dead-letter inspection stay
// attributable to the projection that failed.
func (p *Publisher) PublishRelation(ctx context.Context, task knowledgeprojection.Task) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal relation projection task: %w", err)
	}
	_, err = p.client.EnqueueContext(ctx, asynq.NewTask(TaskTypeRelation, payload), asynq.Queue(QueueProjection), asynq.TaskID(task.QueueID()), asynq.MaxRetry(5))
	if err != nil && err != asynq.ErrTaskIDConflict {
		return fmt.Errorf("enqueue relation projection task: %w", err)
	}
	return nil
}

// RemoveArchived deletes the archived record left behind when the retry budget
// is exhausted. It is idempotent: a missing task or queue means there is
// nothing left to clean.
func (p *Publisher) RemoveArchived(_ context.Context, task ingestion.Task) error {
	if strings.TrimSpace(task.IdempotencyKey) == "" {
		return nil
	}
	err := p.inspector.DeleteTask(poolFor(task), task.IdempotencyKey)
	if err == nil || errors.Is(err, asynq.ErrTaskNotFound) || errors.Is(err, asynq.ErrQueueNotFound) {
		return nil
	}
	return fmt.Errorf("remove archived task %s: %w", task.IdempotencyKey, err)
}

// QueueDepth reports pending and active work per pool. It is the alerting
// signal for a stalled worker, so it must stay independent of task contents.
func (p *Publisher) QueueDepth(ctx context.Context) (map[string]map[string]int, error) {
	result := make(map[string]map[string]int, 4)
	for _, queueName := range []string{QueueIngestion, QueueInteractive, QueueMaintenance, QueueProjection} {
		info, err := p.inspector.GetQueueInfo(queueName)
		if isUncreatedQueue(err) {
			// Asynq creates a queue lazily on first enqueue; an unused pool is
			// an empty pool, not an inspection failure.
			result[queueName] = map[string]int{"pending": 0, "active": 0, "retry": 0, "archived": 0}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect queue %s: %w", queueName, err)
		}
		result[queueName] = map[string]int{"pending": info.Pending, "active": info.Active, "retry": info.Retry, "archived": info.Archived}
	}
	return result, nil
}

// isUncreatedQueue covers both shapes asynq uses for a queue that has never
// been enqueued: GetQueueInfo wraps ErrQueueNotFound for some calls but the
// stats command returns its internal NOT_FOUND error directly.
func isUncreatedQueue(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, asynq.ErrQueueNotFound) {
		return true
	}
	message := err.Error()
	return strings.HasPrefix(message, "NOT_FOUND: queue ") && strings.HasSuffix(message, " does not exist")
}

func poolFor(task ingestion.Task) string {
	switch task.Queue {
	case QueueInteractive, QueueMaintenance:
		return task.Queue
	default:
		return QueueIngestion
	}
}
