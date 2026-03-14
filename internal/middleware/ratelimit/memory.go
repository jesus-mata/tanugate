package ratelimit

import (
	"context"
	"sync"
	"time"
)

const (
	cleanupInterval = 60 * time.Second
	evictionTTL     = 5 * time.Minute
)

// bucket holds the token state for a single rate-limit key.
type bucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	lastAccess time.Time
}

// MemoryLimiter implements the Limiter interface using an in-memory token
// bucket algorithm. Each unique key gets its own bucket. A background
// goroutine evicts idle buckets periodically.
type MemoryLimiter struct {
	buckets sync.Map // key → *bucket
	now     func() time.Time
}

// NewMemoryLimiter creates a MemoryLimiter and starts a background cleanup
// goroutine that stops when ctx is cancelled.
func NewMemoryLimiter(ctx context.Context) *MemoryLimiter {
	ml := &MemoryLimiter{
		now: time.Now,
	}
	go ml.cleanup(ctx)
	return ml
}

// Allow checks whether a request identified by key should be allowed under the
// given limit and window. It uses token bucket semantics: tokens refill at a
// steady rate of limit/window and each request consumes one token.
func (ml *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration, _ Algorithm) (bool, int, time.Time, error) {
	now := ml.now()
	refillRate := float64(limit) / window.Seconds()

	val, _ := ml.buckets.LoadOrStore(key, &bucket{
		tokens:     float64(limit),
		maxTokens:  float64(limit),
		refillRate: refillRate,
		lastRefill: now,
		lastAccess: now,
	})
	b := val.(*bucket)

	b.mu.Lock()
	defer b.mu.Unlock()

	// Update bucket parameters in case the config changed.
	b.maxTokens = float64(limit)
	b.refillRate = refillRate

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.maxTokens {
			b.tokens = b.maxTokens
		}
		b.lastRefill = now
	}

	b.lastAccess = now
	resetAt := now.Add(window)

	if b.tokens >= 1 {
		b.tokens--
		return true, int(b.tokens), resetAt, nil
	}

	return false, 0, resetAt, nil
}

// cleanup periodically evicts buckets that have not been accessed within
// evictionTTL. It exits when ctx is cancelled.
func (ml *MemoryLimiter) cleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ml.evictStale()
		}
	}
}

// evictStale removes buckets whose lastAccess is older than evictionTTL.
func (ml *MemoryLimiter) evictStale() {
	now := ml.now()
	ml.buckets.Range(func(key, value any) bool {
		b := value.(*bucket)
		b.mu.Lock()
		idle := now.Sub(b.lastAccess) > evictionTTL
		b.mu.Unlock()
		if idle {
			ml.buckets.Delete(key)
		}
		return true
	})
}
