// Package ratelimit is a per-key fixed-window limiter. It is deliberately
// in-process: the API is expected to run behind a shared gateway for
// cross-instance limits, and the M4 guarantee is that one tenant cannot consume
// unbounded request throughput on a single instance.
package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	states map[string]*windowState
}

type windowState struct {
	start time.Time
	count int
}

func New(limit int) *Limiter {
	return &Limiter{limit: limit, window: time.Minute, now: time.Now, states: map[string]*windowState{}}
}

// Allow reports whether the key may make another request in the current window.
// A non-positive limit disables limiting.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.states[key]
	if !ok || now.Sub(state.start) >= l.window {
		l.states[key] = &windowState{start: now, count: 1}
		return true
	}
	if state.count >= l.limit {
		return false
	}
	state.count++
	return true
}

// RetryAfter reports how long the caller should wait before the window resets.
func (l *Limiter) RetryAfter() time.Duration {
	if l == nil {
		return 0
	}
	return l.window
}

// Sweep drops expired windows so a long-lived process does not retain one entry
// per tenant forever. It is called opportunistically by the limiter's owner.
func (l *Limiter) Sweep() {
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	for key, state := range l.states {
		if now.Sub(state.start) >= l.window {
			delete(l.states, key)
		}
	}
	l.mu.Unlock()
}
