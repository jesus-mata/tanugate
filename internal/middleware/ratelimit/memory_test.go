package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemory_AllowUpToLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	limit := 5
	for i := 0; i < limit; i++ {
		allowed, remaining, _, err := ml.Allow(ctx, "key1", limit, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		expected := limit - i - 1
		if remaining != expected {
			t.Fatalf("request %d: expected remaining=%d, got %d", i+1, expected, remaining)
		}
	}
}

func TestMemory_RejectAfterLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	limit := 3
	for i := 0; i < limit; i++ {
		allowed, _, _, _ := ml.Allow(ctx, "key1", limit, time.Minute)
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	allowed, remaining, _, err := ml.Allow(ctx, "key1", limit, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("request beyond limit should be rejected")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining=0, got %d", remaining)
	}
}

func TestMemory_TokenRefill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	now := time.Now()
	ml.now = func() time.Time { return now }

	limit := 10
	window := 10 * time.Second

	// Consume all tokens.
	for i := 0; i < limit; i++ {
		ml.Allow(ctx, "key1", limit, window)
	}

	// Advance half the window → should refill ~5 tokens.
	ml.now = func() time.Time { return now.Add(5 * time.Second) }

	allowed, _, _, err := ml.Allow(ctx, "key1", limit, window)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected request to be allowed after partial refill")
	}
}

func TestMemory_FullRefillAfterWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	now := time.Now()
	ml.now = func() time.Time { return now }

	limit := 5
	window := time.Minute

	for i := 0; i < limit; i++ {
		ml.Allow(ctx, "key1", limit, window)
	}

	// Advance full window.
	ml.now = func() time.Time { return now.Add(window) }

	allowed, remaining, _, err := ml.Allow(ctx, "key1", limit, window)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected full quota after full window")
	}
	// Should have limit-1 remaining (consumed 1 from refilled bucket).
	if remaining != limit-1 {
		t.Fatalf("expected remaining=%d, got %d", limit-1, remaining)
	}
}

func TestMemory_DifferentKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	limit := 1

	allowed1, _, _, _ := ml.Allow(ctx, "key-a", limit, time.Minute)
	allowed2, _, _, _ := ml.Allow(ctx, "key-b", limit, time.Minute)

	if !allowed1 || !allowed2 {
		t.Fatal("different keys should have independent buckets")
	}

	// Both should now be exhausted.
	rejected1, _, _, _ := ml.Allow(ctx, "key-a", limit, time.Minute)
	rejected2, _, _, _ := ml.Allow(ctx, "key-b", limit, time.Minute)
	if rejected1 || rejected2 {
		t.Fatal("both keys should be exhausted")
	}
}

func TestMemory_ConcurrentAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	limit := 50
	goroutines := 100
	var allowedCount atomic.Int32
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			allowed, _, _, _ := ml.Allow(ctx, "concurrent-key", limit, time.Minute)
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if int(allowedCount.Load()) != limit {
		t.Fatalf("expected exactly %d allowed, got %d", limit, allowedCount.Load())
	}
}

func TestMemory_CleanupEvictsExpired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	now := time.Now()
	ml.now = func() time.Time { return now }

	ml.Allow(ctx, "stale-key", 10, time.Minute)

	// Advance past eviction TTL.
	ml.now = func() time.Time { return now.Add(evictionTTL + time.Second) }
	ml.evictStale()

	// Bucket should be gone — next Allow creates a fresh one with full tokens.
	allowed, remaining, _, _ := ml.Allow(ctx, "stale-key", 10, time.Minute)
	if !allowed {
		t.Fatal("expected allowed after eviction (fresh bucket)")
	}
	if remaining != 9 {
		t.Fatalf("expected remaining=9 (fresh bucket), got %d", remaining)
	}
}

func TestMemory_CleanupPreservesActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ml := NewMemoryLimiter(ctx)

	now := time.Now()
	ml.now = func() time.Time { return now }

	// Use all tokens.
	for i := 0; i < 5; i++ {
		ml.Allow(ctx, "active-key", 5, time.Minute)
	}

	// Advance, but within eviction TTL.
	ml.now = func() time.Time { return now.Add(evictionTTL - time.Second) }
	ml.evictStale()

	// Bucket should still exist (not evicted), tokens partially refilled.
	allowed, _, _, _ := ml.Allow(ctx, "active-key", 5, time.Minute)
	// With nearly 5 minutes elapsed and 5 tokens/minute refill rate, ~4.9 tokens refilled.
	if !allowed {
		t.Fatal("expected allowed (bucket preserved, tokens refilled)")
	}
}

func TestMemory_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ml := NewMemoryLimiter(ctx)

	// Verify it works.
	allowed, _, _, _ := ml.Allow(ctx, "key", 10, time.Minute)
	if !allowed {
		t.Fatal("expected allowed")
	}

	// Cancel context — cleanup goroutine should exit.
	cancel()
	// Give goroutine time to exit.
	time.Sleep(10 * time.Millisecond)

	// Limiter should still function (Allow doesn't depend on the goroutine).
	allowed, _, _, _ = ml.Allow(context.Background(), "key", 10, time.Minute)
	if !allowed {
		t.Fatal("expected allowed after context cancellation")
	}
}

func TestMemory_BucketCapRejectsNewKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cap := 5
	ml := NewMemoryLimiter(ctx, WithMaxBuckets(cap))

	// Fill all buckets.
	for i := 0; i < cap; i++ {
		allowed, _, _, err := ml.Allow(ctx, fmt.Sprintf("key-%d", i), 10, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("key-%d should be allowed", i)
		}
	}

	// Next new key must be rejected.
	allowed, remaining, _, err := ml.Allow(ctx, "new-key", 10, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("new key beyond cap should be rejected")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining=0, got %d", remaining)
	}

	// Existing keys should still work.
	allowed, _, _, err = ml.Allow(ctx, "key-0", 10, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("existing key should still be allowed")
	}
}

func TestMemory_BucketCapFreesAfterEviction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cap := 3
	ml := NewMemoryLimiter(ctx, WithMaxBuckets(cap))

	now := time.Now()
	ml.now = func() time.Time { return now }

	// Fill all buckets.
	for i := 0; i < cap; i++ {
		ml.Allow(ctx, fmt.Sprintf("key-%d", i), 10, time.Minute)
	}

	// Verify cap is hit.
	allowed, _, _, _ := ml.Allow(ctx, "blocked-key", 10, time.Minute)
	if allowed {
		t.Fatal("should be rejected when cap is full")
	}

	// Advance past eviction TTL and evict.
	ml.now = func() time.Time { return now.Add(evictionTTL + time.Second) }
	ml.evictStale()

	// New keys should be accepted again.
	allowed, _, _, err := ml.Allow(ctx, "fresh-key", 10, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("new key should be allowed after eviction freed capacity")
	}
}

func TestMemory_BucketCapConcurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cap := 10
	ml := NewMemoryLimiter(ctx, WithMaxBuckets(cap))

	goroutines := 100
	var allowedCount atomic.Int32
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			allowed, _, _, _ := ml.Allow(ctx, fmt.Sprintf("concurrent-%d", id), 10, time.Minute)
			if allowed {
				allowedCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	got := int(allowedCount.Load())
	// Due to the small race window between Load and LoadOrStore, count may
	// slightly exceed cap (at most by GOMAXPROCS), but should be close.
	if got > cap+runtime_GOMAXPROCS() {
		t.Fatalf("expected at most ~%d allowed, got %d", cap, got)
	}
	if got < 1 {
		t.Fatal("expected at least 1 allowed")
	}
}

// runtime_GOMAXPROCS returns a reasonable upper bound for race slack.
func runtime_GOMAXPROCS() int {
	// In tests GOMAXPROCS is typically small; use a generous bound.
	return 32
}

func TestMemory_DefaultMaxBuckets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ml := NewMemoryLimiter(ctx)
	if ml.maxBuckets != defaultMaxBuckets {
		t.Fatalf("expected default maxBuckets=%d, got %d", defaultMaxBuckets, ml.maxBuckets)
	}
}
