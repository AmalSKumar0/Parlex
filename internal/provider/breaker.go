package provider

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker is open: provider unavailable")

type BreakerState int

const (
	StateClosed BreakerState = iota
	StateOpen
	StateHalfOpen
)

// CircuitBreaker implements the 5 failures in 30s -> open for 20s breaker.
type CircuitBreaker struct {
	mu           sync.Mutex
	state        BreakerState
	failureTimes []time.Time
	openUntil    time.Time
	maxFailures  int
	window       time.Duration
	coolOff      time.Duration
}

func NewCircuitBreaker(maxFailures int, window, coolOff time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		maxFailures: maxFailures,
		window:      window,
		coolOff:     coolOff,
		state:       StateClosed,
	}
}

func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	if cb.state == StateOpen {
		if now.After(cb.openUntil) {
			cb.state = StateHalfOpen
			return true
		}
		return false
	}
	return true
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = StateClosed
	cb.failureTimes = nil
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	if cb.state == StateHalfOpen {
		cb.state = StateOpen
		cb.openUntil = now.Add(cb.coolOff)
		return
	}

	cutoff := now.Add(-cb.window)
	valid := make([]time.Time, 0, len(cb.failureTimes)+1)
	for _, t := range cb.failureTimes {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	cb.failureTimes = valid

	if len(cb.failureTimes) >= cb.maxFailures {
		cb.state = StateOpen
		cb.openUntil = now.Add(cb.coolOff)
	}
}

func (cb *CircuitBreaker) State() BreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen && time.Now().After(cb.openUntil) {
		return StateHalfOpen
	}
	return cb.state
}
