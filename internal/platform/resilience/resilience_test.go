package resilience

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
)

func TestGuardRetriesRetryableAndStopsOnSuccess(t *testing.T) {
	guard := New(Config{MaxAttempts: 3})
	guard.sleep = func(context.Context, time.Duration) error { return nil }
	var calls int
	err := guard.Do(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return Retryable(errors.New("transient"))
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry = %v after %d calls, want success after 3", err, calls)
	}
}

func TestGuardDoesNotRetryDeterministicFailure(t *testing.T) {
	guard := New(Config{MaxAttempts: 3})
	guard.sleep = func(context.Context, time.Duration) error { return nil }
	var calls int
	err := guard.Do(context.Background(), func(context.Context) error {
		calls++
		return errors.New("bad request")
	})
	if err == nil || calls != 1 {
		t.Fatalf("deterministic failure = %v after %d calls, want one", err, calls)
	}
}

func TestGuardOpensCircuitAfterThreshold(t *testing.T) {
	guard := New(Config{FailureThreshold: 2, MaxAttempts: 1})
	guard.sleep = func(context.Context, time.Duration) error { return nil }
	for i := 0; i < 2; i++ {
		if err := guard.Do(context.Background(), func(context.Context) error {
			return errors.New("provider down")
		}); err == nil {
			t.Fatal("expected provider failure")
		}
	}
	var calls int32
	err := guard.Do(context.Background(), func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) || calls != 0 {
		t.Fatalf("open circuit = %v, calls=%d", err, calls)
	}
}

func TestGuardBulkheadIsPerTenant(t *testing.T) {
	guard := New(Config{MaxConcurrentPerTenant: 1})
	ctxA := tenantdb.WithTenant(context.Background(), "tenant-a")
	releaseA, err := guard.acquire(ctxA)
	if err != nil {
		t.Fatalf("acquire tenant-a: %v", err)
	}
	blockedCtx, cancel := context.WithTimeout(ctxA, 20*time.Millisecond)
	defer cancel()
	if _, err := guard.acquire(blockedCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second tenant-a acquire = %v, want deadline", err)
	}
	ctxB := tenantdb.WithTenant(context.Background(), "tenant-b")
	releaseB, err := guard.acquire(ctxB)
	if err != nil {
		t.Fatalf("tenant-b must not be blocked by tenant-a: %v", err)
	}
	releaseB()
	releaseA()
}
