package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jesus-mata/tanugate/internal/config"
)

func newMiniRedisLimiter(t *testing.T, opts ...func(*config.RedisConfig)) *RedisLimiter {
	t.Helper()
	s := miniredis.RunT(t)
	cfg := &config.RedisConfig{Addr: s.Addr()}
	for _, o := range opts {
		o(cfg)
	}
	rl := NewRedisLimiter(cfg)
	t.Cleanup(func() { _ = rl.Close() })
	return rl
}

func TestAllow_FirstRequestAllowed(t *testing.T) {
	rl := newMiniRedisLimiter(t)

	allowed, remaining, resetAt, err := rl.Allow(context.Background(), "key:first", 10, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	if remaining != 9 {
		t.Fatalf("expected remaining=9, got %d", remaining)
	}
	if resetAt.IsZero() {
		t.Fatal("expected non-zero resetAt")
	}
	if resetAt.Before(time.Now()) {
		t.Fatal("expected resetAt in the future")
	}
}

func TestAllow_ExhaustsLimit(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	limit := 5
	key := "key:exhaust"

	for i := range limit {
		allowed, remaining, _, err := rl.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		if remaining != limit-i-1 {
			t.Fatalf("request %d: expected remaining=%d, got %d", i+1, limit-i-1, remaining)
		}
	}

	// Next request should be rejected.
	allowed, remaining, _, err := rl.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
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

func TestAllow_ResetAtInFuture(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	window := 30 * time.Second
	before := time.Now()

	_, _, resetAt, err := rl.Allow(context.Background(), "key:reset", 10, window, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resetAt should be approximately now + window.
	expectedMin := before.Add(window).Add(-1 * time.Second)
	expectedMax := time.Now().Add(window).Add(1 * time.Second)
	if resetAt.Before(expectedMin) || resetAt.After(expectedMax) {
		t.Fatalf("resetAt=%v out of expected range [%v, %v]", resetAt, expectedMin, expectedMax)
	}
}

func TestAllow_RejectedResetAtBasedOnOldest(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	key := "key:rejected-reset"
	window := time.Minute

	// Exhaust limit.
	for range 3 {
		_, _, _, _ = rl.Allow(ctx, key, 3, window, AlgorithmSlidingWindow)
	}

	_, _, resetAt, err := rl.Allow(ctx, key, 3, window, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resetAt should be based on oldest entry + window, which is roughly now + window.
	if resetAt.Before(time.Now()) {
		t.Fatal("expected resetAt in the future for rejected request")
	}
}

func TestAllow_SeparateKeys(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()

	// Exhaust key A.
	for range 2 {
		_, _, _, _ = rl.Allow(ctx, "key:a", 2, time.Minute, AlgorithmSlidingWindow)
	}

	allowed, _, _, _ := rl.Allow(ctx, "key:a", 2, time.Minute, AlgorithmSlidingWindow)
	if allowed {
		t.Fatal("key:a should be exhausted")
	}

	// key B should be independent.
	allowed, remaining, _, err := rl.Allow(ctx, "key:b", 2, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("key:b should be allowed (independent of key:a)")
	}
	if remaining != 1 {
		t.Fatalf("expected remaining=1, got %d", remaining)
	}
}

func TestAllow_SlidingWindowExpiry(t *testing.T) {
	s := miniredis.RunT(t)
	rl := NewRedisLimiter(&config.RedisConfig{Addr: s.Addr()})
	t.Cleanup(func() { _ = rl.Close() })
	ctx := context.Background()
	key := "key:sliding"
	window := 2 * time.Second

	// Exhaust limit.
	for range 2 {
		_, _, _, _ = rl.Allow(ctx, key, 2, window, AlgorithmSlidingWindow)
	}

	allowed, _, _, _ := rl.Allow(ctx, key, 2, window, AlgorithmSlidingWindow)
	if allowed {
		t.Fatal("should be rejected before window expires")
	}

	// Fast-forward time in miniredis past the window.
	s.FastForward(window + 100*time.Millisecond)

	allowed, remaining, _, err := rl.Allow(ctx, key, 2, window, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("should be allowed after window expires")
	}
	if remaining != 1 {
		t.Fatalf("expected remaining=1, got %d", remaining)
	}
}

func TestAllow_LimitOfOne(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	key := "key:one"

	allowed, remaining, _, err := rl.Allow(ctx, key, 1, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining=0, got %d", remaining)
	}

	allowed, _, _, _ = rl.Allow(ctx, key, 1, time.Minute, AlgorithmSlidingWindow)
	if allowed {
		t.Fatal("second request should be rejected with limit=1")
	}
}

func TestAllow_CancelledContext(t *testing.T) {
	rl := newMiniRedisLimiter(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := rl.Allow(ctx, "key:cancelled", 10, time.Minute, AlgorithmSlidingWindow)
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

func TestAllow_QueryTimeoutApplied(t *testing.T) {
	rl := newMiniRedisLimiter(t, func(cfg *config.RedisConfig) {
		cfg.QueryTimeout = 5 * time.Second
	})

	if rl.queryTimeout != 5*time.Second {
		t.Fatalf("expected queryTimeout=5s, got %v", rl.queryTimeout)
	}

	// Should still succeed — timeout is generous.
	allowed, _, _, err := rl.Allow(context.Background(), "key:timeout", 10, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed")
	}
}

func TestAllow_ZeroQueryTimeoutNoWrapping(t *testing.T) {
	rl := newMiniRedisLimiter(t)

	if rl.queryTimeout != 0 {
		t.Fatalf("expected queryTimeout=0, got %v", rl.queryTimeout)
	}

	allowed, _, _, err := rl.Allow(context.Background(), "key:no-timeout", 10, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed")
	}
}

func TestAllow_ConcurrentAccess(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	limit := 10
	goroutines := 30
	key := "key:concurrent"

	var allowedCount atomic.Int32
	var errCount atomic.Int32
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			allowed, _, _, err := rl.Allow(ctx, key, limit, 10*time.Second, AlgorithmSlidingWindow)
			if err != nil {
				errCount.Add(1)
				return
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Fatalf("expected 0 errors, got %d", errCount.Load())
	}
	if int(allowedCount.Load()) != limit {
		t.Fatalf("expected exactly %d allowed, got %d", limit, allowedCount.Load())
	}
}

func TestAllow_UniqueMembersAcrossCalls(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	key := "key:unique"
	limit := 100

	// Make many calls — if members collide, ZADD overwrites and the count is wrong.
	for i := range limit {
		allowed, _, _, err := rl.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed (limit=%d)", i+1, limit)
		}
	}

	// The next one must be rejected — proving all 100 members were unique.
	allowed, _, _, _ := rl.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
	if allowed {
		t.Fatal("request 101 should be rejected, indicating all prior members were unique")
	}
}

func TestGenerateInstanceID_UniqueAcrossCalls(t *testing.T) {
	id1 := generateInstanceID()
	id2 := generateInstanceID()

	if id1 == "" {
		t.Fatal("instanceID must not be empty")
	}
	if id1 == id2 {
		t.Fatal("consecutive generateInstanceID calls must produce different IDs")
	}
}

func TestAllow_CrossInstanceMemberUniqueness(t *testing.T) {
	// Simulate two gateway instances sharing the same Redis.
	// Both use the same key and limit. Without instanceID, members would
	// collide and ZADD would overwrite, undercounting requests.
	s := miniredis.RunT(t)
	cfg := &config.RedisConfig{Addr: s.Addr()}
	rl1 := NewRedisLimiter(cfg)
	rl2 := NewRedisLimiter(cfg)
	t.Cleanup(func() { _ = rl1.Close(); _ = rl2.Close() })

	ctx := context.Background()
	key := "key:cross-instance"
	limit := 4

	// Instance 1 sends 2 requests.
	for i := range 2 {
		allowed, _, _, err := rl1.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
		if err != nil {
			t.Fatalf("rl1 request %d: unexpected error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("rl1 request %d should be allowed", i+1)
		}
	}

	// Instance 2 sends 2 requests — both should count against the same key.
	for i := range 2 {
		allowed, _, _, err := rl2.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
		if err != nil {
			t.Fatalf("rl2 request %d: unexpected error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("rl2 request %d should be allowed", i+1)
		}
	}

	// The 5th request from either instance must be rejected.
	allowed, _, _, err := rl1.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("5th request should be rejected — proves all 4 cross-instance members were unique")
	}
}

func TestAllow_ConnectionRefused(t *testing.T) {
	// Point at an address where nothing is listening.
	rl := NewRedisLimiter(&config.RedisConfig{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rl.Close() })

	_, _, _, err := rl.Allow(context.Background(), "key:refused", 10, time.Minute, AlgorithmSlidingWindow)
	if err == nil {
		t.Fatal("expected error when Redis is unreachable")
	}
}

func TestAllow_HighLimit(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	key := "key:high-limit"
	limit := 1000

	allowed, remaining, _, err := rl.Allow(ctx, key, limit, time.Minute, AlgorithmSlidingWindow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed")
	}
	if remaining != limit-1 {
		t.Fatalf("expected remaining=%d, got %d", limit-1, remaining)
	}
}

// --- Leaky Bucket Tests ---

func newMiniRedisLeakyLimiter(t *testing.T) (*RedisLimiter, *time.Time) {
	t.Helper()
	s := miniredis.RunT(t)
	cfg := &config.RedisConfig{Addr: s.Addr()}
	rl := NewRedisLimiter(cfg)
	now := time.Now()
	rl.now = func() time.Time { return now }
	t.Cleanup(func() { _ = rl.Close() })
	return rl, &now
}

func TestLeakyBucket_FirstRequestAllowed(t *testing.T) {
	rl, _ := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()

	allowed, remaining, resetAt, err := rl.Allow(ctx, "lb:first", 10, time.Minute, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	if remaining != 9 {
		t.Fatalf("expected remaining=9, got %d", remaining)
	}
	if resetAt.IsZero() {
		t.Fatal("expected non-zero resetAt")
	}
}

func TestLeakyBucket_ExhaustsCapacity(t *testing.T) {
	rl, _ := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	capacity := 5
	key := "lb:exhaust"

	for i := range capacity {
		allowed, remaining, _, err := rl.Allow(ctx, key, capacity, time.Minute, AlgorithmLeakyBucket)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		if remaining != capacity-i-1 {
			t.Fatalf("request %d: expected remaining=%d, got %d", i+1, capacity-i-1, remaining)
		}
	}

	// Next request should be rejected.
	allowed, remaining, _, err := rl.Allow(ctx, key, capacity, time.Minute, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("request beyond capacity should be rejected")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining=0, got %d", remaining)
	}
}

func TestLeakyBucket_LeaksOverTime(t *testing.T) {
	rl, nowPtr := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	capacity := 5
	window := 10 * time.Second
	key := "lb:leak"

	// Fill bucket completely.
	for range capacity {
		_, _, _, _ = rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	}

	// Verify bucket is full.
	allowed, _, _, _ := rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if allowed {
		t.Fatal("bucket should be full")
	}

	// Advance time by half the window → should leak ~half capacity.
	*nowPtr = nowPtr.Add(5 * time.Second)

	allowed, _, _, err := rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected request to be allowed after partial drain")
	}
}

func TestLeakyBucket_FullDrainAfterWindow(t *testing.T) {
	rl, nowPtr := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	capacity := 5
	window := time.Minute
	key := "lb:full-drain"

	// Fill bucket.
	for range capacity {
		_, _, _, _ = rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	}

	// Advance full window → bucket should be empty.
	*nowPtr = nowPtr.Add(window)

	allowed, remaining, _, err := rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed after full drain")
	}
	if remaining != capacity-1 {
		t.Fatalf("expected remaining=%d, got %d", capacity-1, remaining)
	}
}

func TestLeakyBucket_SeparateKeys(t *testing.T) {
	rl, _ := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()

	// Exhaust key A.
	for range 2 {
		_, _, _, _ = rl.Allow(ctx, "lb:a", 2, time.Minute, AlgorithmLeakyBucket)
	}
	allowed, _, _, _ := rl.Allow(ctx, "lb:a", 2, time.Minute, AlgorithmLeakyBucket)
	if allowed {
		t.Fatal("lb:a should be exhausted")
	}

	// key B should be independent.
	allowed, remaining, _, err := rl.Allow(ctx, "lb:b", 2, time.Minute, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("lb:b should be allowed (independent of lb:a)")
	}
	if remaining != 1 {
		t.Fatalf("expected remaining=1, got %d", remaining)
	}
}

func TestLeakyBucket_ConcurrentAccess(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	capacity := 10
	goroutines := 30
	key := "lb:concurrent"

	var allowedCount atomic.Int32
	var errCount atomic.Int32
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			allowed, _, _, err := rl.Allow(ctx, key, capacity, 10*time.Second, AlgorithmLeakyBucket)
			if err != nil {
				errCount.Add(1)
				return
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Fatalf("expected 0 errors, got %d", errCount.Load())
	}
	if int(allowedCount.Load()) != capacity {
		t.Fatalf("expected exactly %d allowed, got %d", capacity, allowedCount.Load())
	}
}

func TestLeakyBucket_ResetAtInFuture(t *testing.T) {
	rl, nowPtr := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	now := *nowPtr

	_, _, resetAt, err := rl.Allow(ctx, "lb:reset", 10, time.Minute, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resetAt.After(now) {
		t.Fatalf("expected resetAt after now, got resetAt=%v, now=%v", resetAt, now)
	}
}

func TestLeakyBucket_RejectedResetAt(t *testing.T) {
	rl, nowPtr := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	capacity := 3
	window := 30 * time.Second
	key := "lb:rejected-reset"
	now := *nowPtr

	// Fill bucket.
	for range capacity {
		_, _, _, _ = rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	}

	// Rejected request.
	_, _, resetAt, err := rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// resetAt should be now + time for 1 unit to leak.
	// leak_rate = capacity / window_ms = 3 / 30000 = 0.0001 per ms
	// time for 1 unit = 1 / leak_rate = 10000 ms = 10s
	expectedReset := now.Add(10 * time.Second)
	if resetAt.Before(expectedReset.Add(-100*time.Millisecond)) || resetAt.After(expectedReset.Add(100*time.Millisecond)) {
		t.Fatalf("expected resetAt≈%v, got %v", expectedReset, resetAt)
	}
}

func TestLeakyBucket_KeyExpiry(t *testing.T) {
	s := miniredis.RunT(t)
	rl := NewRedisLimiter(&config.RedisConfig{Addr: s.Addr()})
	t.Cleanup(func() { _ = rl.Close() })
	ctx := context.Background()
	key := "lb:expiry"
	window := 2 * time.Second

	_, _, _, _ = rl.Allow(ctx, key, 10, window, AlgorithmLeakyBucket)

	// Verify key exists.
	if !s.Exists(key) {
		t.Fatal("expected key to exist after Allow")
	}

	// Fast-forward past the window to trigger PEXPIRE.
	s.FastForward(window + 100*time.Millisecond)

	if s.Exists(key) {
		t.Fatal("expected key to be expired after window")
	}
}

func TestLeakyBucket_DrainPersistsAcrossRejections(t *testing.T) {
	rl, nowPtr := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	capacity := 5
	window := 10 * time.Second // leak_rate = 5/10000 = 0.0005/ms → 1 unit drains in 2s
	key := "lb:reject-drain"

	// Fill bucket at t=0.
	for range capacity {
		_, _, _, _ = rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	}

	// Reject at t=1s — should drain 0.5 units (level 5→4.5) and persist it.
	*nowPtr = nowPtr.Add(1 * time.Second)
	allowed, _, _, _ := rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if allowed {
		t.Fatal("should be rejected at t=1s (level ≈ 4.5)")
	}

	// Reject at t=1.5s — should drain another 0.25 (level 4.5→4.25).
	*nowPtr = nowPtr.Add(500 * time.Millisecond)
	allowed, _, _, _ = rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if allowed {
		t.Fatal("should be rejected at t=1.5s (level ≈ 4.25)")
	}

	// At t=2s, total drain = 2s * 0.5/s = 1.0 unit. Level should be ~4.0.
	// 4.0 + 1 <= 5 → should be allowed.
	*nowPtr = nowPtr.Add(500 * time.Millisecond)
	allowed, _, _, err := rl.Allow(ctx, key, capacity, window, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("should be allowed at t=2s (cumulative drain = 1.0 unit, level ≈ 4.0 + 1 = 5.0 <= 5)")
	}
}

func TestLeakyBucket_LimitOfOne(t *testing.T) {
	rl, _ := newMiniRedisLeakyLimiter(t)
	ctx := context.Background()
	key := "lb:one"

	allowed, remaining, _, err := rl.Allow(ctx, key, 1, time.Minute, AlgorithmLeakyBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining=0, got %d", remaining)
	}

	allowed, _, _, _ = rl.Allow(ctx, key, 1, time.Minute, AlgorithmLeakyBucket)
	if allowed {
		t.Fatal("second request should be rejected with capacity=1")
	}
}
