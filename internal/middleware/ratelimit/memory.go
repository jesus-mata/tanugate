package ratelimit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	cleanupInterval   = 60 * time.Second
	evictionTTL       = 5 * time.Minute
	defaultMaxBuckets = 100_000
)

// Option configures a MemoryLimiter.
type Option func(*MemoryLimiter)

// WithMaxBuckets sets the maximum number of rate-limit buckets. When the cap
// is reached, new keys are rejected (fail-closed) to prevent memory exhaustion.
func WithMaxBuckets(n int) Option {
	return func(ml *MemoryLimiter) { ml.maxBuckets = n }
}

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
	buckets    sync.Map // key → *bucket
	count      atomic.Int64
	maxBuckets int
	now        func() time.Time
}

// NewMemoryLimiter creates a MemoryLimiter and starts a background cleanup
// goroutine that stops when ctx is cancelled.
func NewMemoryLimiter(ctx context.Context, opts ...Option) *MemoryLimiter {
	ml := &MemoryLimiter{
		maxBuckets: defaultMaxBuckets,
		now:        time.Now,
	}
	for _, o := range opts {
		o(ml)
	}
	go ml.cleanup(ctx)
	return ml
}

// Allow checks whether a request identified by key should be allowed under the
// given limit and window. It uses token bucket semantics: tokens refill at a
// steady rate of limit/window and each request consumes one token.
func (ml *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, int, time.Time, error) {
	now := ml.now()
	refillRate := float64(limit) / window.Seconds()

	var b *bucket
	if val, ok := ml.buckets.Load(key); ok {
		// Fast path: existing key (no allocation).
		b = val.(*bucket)
	} else {
		// Check cap before creating a new bucket.
		if ml.count.Load() >= int64(ml.maxBuckets) {
			slog.Warn("rate limiter bucket cap reached, rejecting new key",
				"key", key, "max_buckets", ml.maxBuckets)
			return false, 0, now.Add(window), nil
		}
		val, loaded := ml.buckets.LoadOrStore(key, &bucket{
			tokens:     float64(limit),
			maxTokens:  float64(limit),
			refillRate: refillRate,
			lastRefill: now,
			lastAccess: now,
		})
		b = val.(*bucket)
		if !loaded {
			ml.count.Add(1)
		}
	}

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
			ml.count.Add(-1)
		}
		return true
	})
}
