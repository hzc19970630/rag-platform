// Package circuitbreaker provides a minimal in-process breaker for calls to
// external model providers. Consecutive failures open the breaker; after the
// cooldown a single half-open probe decides whether to close it again. Callers
// keep serving degraded results while the breaker is open.
package circuitbreaker

import (
	"sync"
	"time"
)

const (
	DefaultFailureThreshold = 5
	DefaultCooldown         = 30 * time.Second
)

type Config struct {
	// FailureThreshold is the number of consecutive failures that opens the
	// breaker. Values <= 0 use DefaultFailureThreshold.
	FailureThreshold int
	// Cooldown is how long the breaker stays open before admitting a probe.
	// Values <= 0 use DefaultCooldown.
	Cooldown time.Duration
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time
}

type state int

const (
	stateClosed state = iota
	stateOpen
	stateHalfOpen
)

type Breaker struct {
	mu sync.Mutex

	failureThreshold int
	cooldown         time.Duration
	now              func() time.Time

	state    state
	failures int
	openedAt time.Time
	probing  bool
}

func New(config Config) *Breaker {
	threshold := config.FailureThreshold
	if threshold <= 0 {
		threshold = DefaultFailureThreshold
	}
	cooldown := config.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Breaker{failureThreshold: threshold, cooldown: cooldown, now: now}
}

// Allow reports whether the caller may make a provider call. An open breaker
// admits one probe once the cooldown elapsed; a half-open breaker admits
// nothing while that probe is still in flight.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateOpen:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		b.state = stateHalfOpen
		b.probing = true
		return true
	case stateHalfOpen:
		if b.probing {
			return false
		}
		b.probing = true
		return true
	default:
		return true
	}
}

// Success closes the breaker and clears the consecutive failure count.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = stateClosed
	b.failures = 0
	b.probing = false
}

// Failure records a provider failure. A failed half-open probe reopens the
// breaker and restarts the cooldown.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == stateHalfOpen {
		b.open()
		return
	}
	b.failures++
	if b.failures >= b.failureThreshold {
		b.open()
	}
}

// Release ends a half-open probe without a verdict, for example when the
// caller's context was canceled. The next request may probe again immediately.
func (b *Breaker) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

func (b *Breaker) open() {
	b.state = stateOpen
	b.openedAt = b.now()
	b.failures = 0
	b.probing = false
}
