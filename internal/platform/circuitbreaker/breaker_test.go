package circuitbreaker

import (
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(duration time.Duration) { c.now = c.now.Add(duration) }

func newTestBreaker(threshold int, cooldown time.Duration) (*Breaker, *fakeClock) {
	clock := &fakeClock{now: time.Unix(1700000000, 0)}
	return New(Config{FailureThreshold: threshold, Cooldown: cooldown, Now: clock.Now}), clock
}

func TestNewAppliesDefaults(t *testing.T) {
	breaker := New(Config{})
	if breaker.failureThreshold != DefaultFailureThreshold {
		t.Fatalf("failure threshold = %d, want %d", breaker.failureThreshold, DefaultFailureThreshold)
	}
	if breaker.cooldown != DefaultCooldown {
		t.Fatalf("cooldown = %v, want %v", breaker.cooldown, DefaultCooldown)
	}
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	breaker, clock := newTestBreaker(3, 30*time.Second)
	for i := 0; i < 3; i++ {
		if !breaker.Allow() {
			t.Fatalf("call %d must be allowed while closed", i+1)
		}
		breaker.Failure()
	}
	if breaker.Allow() {
		t.Fatalf("an open breaker must reject calls before the cooldown")
	}
	clock.Advance(30 * time.Second)
	if !breaker.Allow() {
		t.Fatalf("the cooldown must admit a half-open probe")
	}
	if breaker.Allow() {
		t.Fatalf("a half-open probe must be exclusive")
	}
}

func TestBreakerHalfOpenSuccessCloses(t *testing.T) {
	breaker, clock := newTestBreaker(2, time.Minute)
	breaker.Failure()
	breaker.Failure()
	clock.Advance(time.Minute)
	if !breaker.Allow() {
		t.Fatalf("expected a probe after the cooldown")
	}
	breaker.Success()
	if !breaker.Allow() {
		t.Fatalf("a successful probe must close the breaker")
	}
	// The consecutive failure count must reset with the close: one failure on a
	// two-failure threshold stays closed.
	breaker.Failure()
	if !breaker.Allow() {
		t.Fatalf("closed breaker must stay closed below the threshold")
	}
}

func TestBreakerHalfOpenFailureRestartsCooldown(t *testing.T) {
	breaker, clock := newTestBreaker(1, time.Minute)
	breaker.Failure()
	clock.Advance(2 * time.Minute)
	if !breaker.Allow() {
		t.Fatalf("expected a probe after the cooldown")
	}
	breaker.Failure()
	if breaker.Allow() {
		t.Fatalf("a failed probe must reopen the breaker")
	}
	clock.Advance(time.Minute - time.Nanosecond)
	if breaker.Allow() {
		t.Fatalf("a failed probe must restart the cooldown")
	}
	clock.Advance(time.Nanosecond)
	if !breaker.Allow() {
		t.Fatalf("the restarted cooldown must admit the next probe")
	}
}

func TestBreakerReleaseAllowsAnotherProbeWithoutCounting(t *testing.T) {
	breaker, clock := newTestBreaker(2, time.Minute)
	breaker.Failure()
	breaker.Failure()
	clock.Advance(time.Minute)
	if !breaker.Allow() {
		t.Fatalf("expected a probe after the cooldown")
	}
	// A canceled probe has no verdict: release it and let the next request try.
	breaker.Release()
	if !breaker.Allow() {
		t.Fatalf("a released probe must admit another probe")
	}
	breaker.Success()
	breaker.Failure()
	if !breaker.Allow() {
		t.Fatalf("release must not leave the breaker open")
	}
}
