package ratelimit

import (
	"context"
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
		allowed, remaining, _, err := ml.Allow(ctx, "key1", limit, time.Minute, "")
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
		allowed, _, _, _ := ml.Allow(ctx, "key1", limit, time.Minute, "")
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	allowed, remaining, _, err := ml.Allow(ctx, "key1", limit, time.Minute, "")
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
		_, _, _, _ = ml.Allow(ctx, "key1", limit, window, "")
	}

	// Advance half the window → should refill ~5 tokens.
	ml.now = func() time.Time { return now.Add(5 * time.Second) }

	allowed, _, _, err := ml.Allow(ctx, "key1", limit, window, "")
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
		_, _, _, _ = ml.Allow(ctx, "key1", limit, window, "")
	}

	// Advance full window.
	ml.now = func() time.Time { return now.Add(window) }

	allowed, remaining, _, err := ml.Allow(ctx, "key1", limit, window, "")
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

	allowed1, _, _, _ := ml.Allow(ctx, "key-a", limit, time.Minute, "")
	allowed2, _, _, _ := ml.Allow(ctx, "key-b", limit, time.Minute, "")

	if !allowed1 || !allowed2 {
		t.Fatal("different keys should have independent buckets")
	}

	// Both should now be exhausted.
	rejected1, _, _, _ := ml.Allow(ctx, "key-a", limit, time.Minute, "")
	rejected2, _, _, _ := ml.Allow(ctx, "key-b", limit, time.Minute, "")
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
			allowed, _, _, _ := ml.Allow(ctx, "concurrent-key", limit, time.Minute, "")
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

	_, _, _, _ = ml.Allow(ctx, "stale-key", 10, time.Minute, "")

	// Advance past eviction TTL.
	ml.now = func() time.Time { return now.Add(evictionTTL + time.Second) }
	ml.evictStale()

	// Bucket should be gone — next Allow creates a fresh one with full tokens.
	allowed, remaining, _, _ := ml.Allow(ctx, "stale-key", 10, time.Minute, "")
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
		_, _, _, _ = ml.Allow(ctx, "active-key", 5, time.Minute, "")
	}

	// Advance, but within eviction TTL.
	ml.now = func() time.Time { return now.Add(evictionTTL - time.Second) }
	ml.evictStale()

	// Bucket should still exist (not evicted), tokens partially refilled.
	allowed, _, _, _ := ml.Allow(ctx, "active-key", 5, time.Minute, "")
	// With nearly 5 minutes elapsed and 5 tokens/minute refill rate, ~4.9 tokens refilled.
	if !allowed {
		t.Fatal("expected allowed (bucket preserved, tokens refilled)")
	}
}

func TestMemory_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ml := NewMemoryLimiter(ctx)

	// Verify it works.
	allowed, _, _, _ := ml.Allow(ctx, "key", 10, time.Minute, "")
	if !allowed {
		t.Fatal("expected allowed")
	}

	// Cancel context — cleanup goroutine should exit.
	cancel()
	// Give goroutine time to exit.
	time.Sleep(10 * time.Millisecond)

	// Limiter should still function (Allow doesn't depend on the goroutine).
	allowed, _, _, _ = ml.Allow(context.Background(), "key", 10, time.Minute, "")
	if !allowed {
		t.Fatal("expected allowed after context cancellation")
	}
}
