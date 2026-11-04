package queue

import (
	"errors"
	"fmt"
	"testing"

	"github.com/example/rag-platform/contracts/tasks"
	"github.com/hibiken/asynq"
)

func TestPoolForIsolatesOperatorAndBulkWork(t *testing.T) {
	tests := map[string]string{
		"":            QueueIngestion,
		"unexpected":  QueueIngestion,
		"ingestion":   QueueIngestion,
		"interactive": QueueInteractive,
		"maintenance": QueueMaintenance,
	}
	for declared, want := range tests {
		if got := poolFor(tasks.Ingestion{Queue: declared}); got != want {
			t.Fatalf("poolFor(%q) = %q, want %q", declared, got, want)
		}
	}
}

func TestIsUncreatedQueue(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"wrapped sentinel", fmt.Errorf("asynq: %w", asynq.ErrQueueNotFound), true},
		{"stats not found", errors.New(`NOT_FOUND: queue "maintenance" does not exist`), true},
		{"other redis error", errors.New("connection refused"), false},
	}
	for _, test := range tests {
		if got := isUncreatedQueue(test.err); got != test.want {
			t.Fatalf("isUncreatedQueue(%s) = %v, want %v", test.name, got, test.want)
		}
	}
}
