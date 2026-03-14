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

	allowed, remaining, resetAt, err := rl.Allow(context.Background(), "key:first", 10, time.Minute)
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
		allowed, remaining, _, err := rl.Allow(ctx, key, limit, time.Minute)
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
	allowed, remaining, _, err := rl.Allow(ctx, key, limit, time.Minute)
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

	_, _, resetAt, err := rl.Allow(context.Background(), "key:reset", 10, window)
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
		_, _, _, _ = rl.Allow(ctx, key, 3, window)
	}

	_, _, resetAt, err := rl.Allow(ctx, key, 3, window)
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
		_, _, _, _ = rl.Allow(ctx, "key:a", 2, time.Minute)
	}

	allowed, _, _, _ := rl.Allow(ctx, "key:a", 2, time.Minute)
	if allowed {
		t.Fatal("key:a should be exhausted")
	}

	// key B should be independent.
	allowed, remaining, _, err := rl.Allow(ctx, "key:b", 2, time.Minute)
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
		_, _, _, _ = rl.Allow(ctx, key, 2, window)
	}

	allowed, _, _, _ := rl.Allow(ctx, key, 2, window)
	if allowed {
		t.Fatal("should be rejected before window expires")
	}

	// Fast-forward time in miniredis past the window.
	s.FastForward(window + 100*time.Millisecond)

	allowed, remaining, _, err := rl.Allow(ctx, key, 2, window)
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

	allowed, remaining, _, err := rl.Allow(ctx, key, 1, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining=0, got %d", remaining)
	}

	allowed, _, _, _ = rl.Allow(ctx, key, 1, time.Minute)
	if allowed {
		t.Fatal("second request should be rejected with limit=1")
	}
}

func TestAllow_CancelledContext(t *testing.T) {
	rl := newMiniRedisLimiter(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := rl.Allow(ctx, "key:cancelled", 10, time.Minute)
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
	allowed, _, _, err := rl.Allow(context.Background(), "key:timeout", 10, time.Minute)
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

	allowed, _, _, err := rl.Allow(context.Background(), "key:no-timeout", 10, time.Minute)
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
			allowed, _, _, err := rl.Allow(ctx, key, limit, 10*time.Second)
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
		allowed, _, _, err := rl.Allow(ctx, key, limit, time.Minute)
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("request %d should be allowed (limit=%d)", i+1, limit)
		}
	}

	// The next one must be rejected — proving all 100 members were unique.
	allowed, _, _, _ := rl.Allow(ctx, key, limit, time.Minute)
	if allowed {
		t.Fatal("request 101 should be rejected, indicating all prior members were unique")
	}
}

func TestAllow_ConnectionRefused(t *testing.T) {
	// Point at an address where nothing is listening.
	rl := NewRedisLimiter(&config.RedisConfig{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rl.Close() })

	_, _, _, err := rl.Allow(context.Background(), "key:refused", 10, time.Minute)
	if err == nil {
		t.Fatal("expected error when Redis is unreachable")
	}
}

func TestAllow_HighLimit(t *testing.T) {
	rl := newMiniRedisLimiter(t)
	ctx := context.Background()
	key := "key:high-limit"
	limit := 1000

	allowed, remaining, _, err := rl.Allow(ctx, key, limit, time.Minute)
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
