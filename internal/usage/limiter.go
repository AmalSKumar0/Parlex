package usage

import (
	"sync"
	"time"
)

// TokenBucket implements a simple thread-safe token bucket rate limiter.
type TokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// NewTokenBucket creates a token bucket with given capacity and refill duration.
// For example, 10 tokens per 1 minute -> capacity: 10, refillRate: 10/60.
func NewTokenBucket(capacity int, window time.Duration) *TokenBucket {
	capFloat := float64(capacity)
	return &TokenBucket{
		capacity:   capFloat,
		tokens:     capFloat,
		refillRate: capFloat / window.Seconds(),
		lastRefill: time.Now(),
	}
}

// Allow attempts to consume 1 token. Returns true if allowed, false otherwise.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}

	return false
}

// KeyedRateLimiter maintains a token bucket per key (IP, device_id, etc.).
type KeyedRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*TokenBucket
	capacity int
	window   time.Duration
}

// NewKeyedRateLimiter creates a rate limiter for keys.
func NewKeyedRateLimiter(capacity int, window time.Duration) *KeyedRateLimiter {
	return &KeyedRateLimiter{
		limiters: make(map[string]*TokenBucket),
		capacity: capacity,
		window:   window,
	}
}

// Allow checks if the request for key is allowed.
func (k *KeyedRateLimiter) Allow(key string) bool {
	k.mu.Lock()
	limiter, exists := k.limiters[key]
	if !exists {
		limiter = NewTokenBucket(k.capacity, k.window)
		k.limiters[key] = limiter
	}
	k.mu.Unlock()

	return limiter.Allow()
}

// ClaimFailureTracker tracks consecutive failed claim attempts and enforces lockout.
type ClaimFailureTracker struct {
	mu              sync.Mutex
	failures        map[string]int
	lockouts        map[string]time.Time
	maxFailures     int
	lockoutDuration time.Duration
}

// NewClaimFailureTracker creates a new tracker with failure limit and lockout.
func NewClaimFailureTracker(maxFailures int, lockoutDuration time.Duration) *ClaimFailureTracker {
	return &ClaimFailureTracker{
		failures:        make(map[string]int),
		lockouts:        make(map[string]time.Time),
		maxFailures:     maxFailures,
		lockoutDuration: lockoutDuration,
	}
}

// IsLockedOut returns true if the IP is currently in lockout.
func (c *ClaimFailureTracker) IsLockedOut(ip string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	until, locked := c.lockouts[ip]
	if !locked {
		return false
	}

	if time.Now().After(until) {
		delete(c.lockouts, ip)
		delete(c.failures, ip)
		return false
	}

	return true
}

// RecordFailure records a failed claim attempt and locks out if threshold reached.
func (c *ClaimFailureTracker) RecordFailure(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failures[ip]++
	if c.failures[ip] >= c.maxFailures {
		c.lockouts[ip] = time.Now().Add(c.lockoutDuration)
	}
}

// RecordSuccess resets failures on successful claim.
func (c *ClaimFailureTracker) RecordSuccess(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.failures, ip)
	delete(c.lockouts, ip)
}
