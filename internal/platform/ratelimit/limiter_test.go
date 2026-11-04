package ratelimit

import (
	"testing"
	"time"
)

func TestAllowStopsAtLimitAndResetsWithWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := New(2)
	limiter.now = func() time.Time { return now }

	if !limiter.Allow("tenant-a") || !limiter.Allow("tenant-a") {
		t.Fatal("the first requests in a window must be allowed")
	}
	if limiter.Allow("tenant-a") {
		t.Fatal("the request beyond the limit must be rejected")
	}
	if !limiter.Allow("tenant-b") {
		t.Fatal("limits must be per key, not global")
	}

	now = now.Add(time.Minute)
	if !limiter.Allow("tenant-a") {
		t.Fatal("a new window must reset the counter")
	}
}

func TestNonPositiveLimitDisablesLimiting(t *testing.T) {
	for _, limit := range []int{0, -1} {
		limiter := New(limit)
		for index := 0; index < 1000; index++ {
			if !limiter.Allow("tenant") {
				t.Fatalf("limit %d must disable limiting", limit)
			}
		}
	}
	var nilLimiter *Limiter
	if !nilLimiter.Allow("tenant") {
		t.Fatal("a nil limiter must allow requests")
	}
}

func TestAllowSweepsExpiredWindows(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := New(1)
	limiter.now = func() time.Time { return now }
	limiter.Allow("expired")
	now = now.Add(30 * time.Second)
	limiter.Allow("current")

	// The next request after a full window triggers the sweep without any
	// owner or background goroutine calling it explicitly.
	now = now.Add(31 * time.Second)
	limiter.Allow("new")
	if _, ok := limiter.states["expired"]; ok {
		t.Fatal("the sweep must drop expired windows")
	}
	if _, ok := limiter.states["current"]; !ok {
		t.Fatal("the sweep must keep windows still inside the current window")
	}
	if _, ok := limiter.states["new"]; !ok {
		t.Fatal("the triggering request must still be tracked")
	}
	if limiter.RetryAfter() != time.Minute {
		t.Fatalf("RetryAfter = %s, want 1m", limiter.RetryAfter())
	}
}
