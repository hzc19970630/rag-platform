package outbox

import (
	"context"
	"testing"
	"time"
)

func TestDecodeJSON(t *testing.T) {
	decode := DecodeJSON[testTask]()
	value, err := decode([]byte(`{"n":7}`))
	if err != nil || value.(testTask).N != 7 {
		t.Fatalf("DecodeJSON = %+v, %v", value, err)
	}
	if _, err := decode([]byte("{bad")); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestRunPublishesUntilCancelled(t *testing.T) {
	published := make(chan struct{}, 1)
	publisher, db := testPublisher(t, 1, func(context.Context, testTask) error {
		select {
		case published <- struct{}{}:
		default:
		}
		return nil
	})
	publisher.options.Interval = 5 * time.Millisecond
	seedEvents(t, db, `{"n":1}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		publisher.Run(ctx)
		close(done)
	}()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("Run did not publish the pending event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}
