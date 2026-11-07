// Package resilience owns the guard shared by model adapters: one circuit
// breaker per adapter, a per-tenant concurrency bulkhead, and a bounded retry
// policy for transient provider failures. The queue's retry budget is separate;
// these retries never outlive a single stage attempt.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/circuitbreaker"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
)

// ErrCircuitOpen is returned without calling the provider while the breaker is
// open. Callers can degrade (rerank) or fail the request (chat/embedding).
var ErrCircuitOpen = errors.New("model provider circuit is open")

// Config is the shared budget. Zero values fall back to the defaults.
type Config struct {
	FailureThreshold       int
	Cooldown               time.Duration
	MaxConcurrentPerTenant int
	MaxAttempts            int
	BaseBackoff            time.Duration
}

func DefaultConfig() Config {
	return Config{
		FailureThreshold:       5,
		Cooldown:               30 * time.Second,
		MaxConcurrentPerTenant: 4,
		MaxAttempts:            2,
		BaseBackoff:            200 * time.Millisecond,
	}
}

func (c Config) withDefaults() Config {
	defaults := DefaultConfig()
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = defaults.FailureThreshold
	}
	if c.Cooldown <= 0 {
		c.Cooldown = defaults.Cooldown
	}
	if c.MaxConcurrentPerTenant <= 0 {
		c.MaxConcurrentPerTenant = defaults.MaxConcurrentPerTenant
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaults.MaxAttempts
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = defaults.BaseBackoff
	}
	return c
}

type Guard struct {
	cfg     Config
	breaker *circuitbreaker.Breaker
	mu      sync.Mutex
	tenants map[string]chan struct{}
	sleep   func(context.Context, time.Duration) error
}

func New(cfg Config) *Guard {
	cfg = cfg.withDefaults()
	return &Guard{
		cfg:     cfg,
		breaker: circuitbreaker.New(circuitbreaker.Config{FailureThreshold: cfg.FailureThreshold, Cooldown: cfg.Cooldown}),
		tenants: map[string]chan struct{}{},
		sleep:   sleepContext,
	}
}

// Do runs one provider operation under the breaker, the tenant bulkhead and
// the retry policy. Retryable errors are wrapped with Retryable by the adapter.
func (g *Guard) Do(ctx context.Context, fn func(context.Context) error) error {
	if !g.breaker.Allow() {
		return ErrCircuitOpen
	}
	release, err := g.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	var last error
	for attempt := 1; attempt <= g.cfg.MaxAttempts; attempt++ {
		last = fn(ctx)
		if last == nil {
			g.breaker.Success()
			return nil
		}
		if !IsRetryable(last) || attempt == g.cfg.MaxAttempts || ctx.Err() != nil {
			break
		}
		delay := g.cfg.BaseBackoff << (attempt - 1)
		if err := g.sleep(ctx, delay); err != nil {
			last = err
			break
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	g.breaker.Failure()
	return last
}

func (g *Guard) acquire(ctx context.Context) (func(), error) {
	key, ok := tenantdb.TenantFrom(ctx)
	if !ok {
		key = "_unscoped"
	}
	g.mu.Lock()
	semaphore, exists := g.tenants[key]
	if !exists {
		semaphore = make(chan struct{}, g.cfg.MaxConcurrentPerTenant)
		g.tenants[key] = semaphore
	}
	g.mu.Unlock()
	select {
	case semaphore <- struct{}{}:
		return func() { <-semaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RetryableError marks a provider failure that is safe to retry (transport,
// 429 or 5xx). Deterministic 4xx and decode errors stay unwrapped.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return e.Err.Error() }

func (e *RetryableError) Unwrap() error { return e.Err }

func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err}
}

func IsRetryable(err error) bool {
	var target *RetryableError
	return errors.As(err, &target)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ProviderStatusError classifies an HTTP status for the adapters without
// leaking the provider response body into the retry decision.
func ProviderStatusError(status int, message string) error {
	err := fmt.Errorf("model provider returned %d: %s", status, message)
	if status == 429 || status >= 500 {
		return Retryable(err)
	}
	return err
}
