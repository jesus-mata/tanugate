package ratelimit

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
)

func redisAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set, skipping Redis tests")
	}
	return addr
}

func newTestRedisLimiter(t *testing.T) *RedisLimiter {
	t.Helper()
	addr := redisAddr(t)
	rl := NewRedisLimiter(&config.RedisConfig{Addr: addr})
	t.Cleanup(func() { _ = rl.Close() })
	return rl
}

func TestRedis_AllowUpToLimit(t *testing.T) {
	rl := newTestRedisLimiter(t)
	ctx := context.Background()
	limit := 5
	key := "test:allow:" + t.Name()

	for i := 0; i < limit; i++ {
		allowed, remaining, _, err := rl.Allow(ctx, key, limit, 10*time.Second)
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

func TestRedis_RejectAfterLimit(t *testing.T) {
	rl := newTestRedisLimiter(t)
	ctx := context.Background()
	limit := 3
	key := "test:reject:" + t.Name()

	for i := 0; i < limit; i++ {
		allowed, _, _, _ := rl.Allow(ctx, key, limit, 10*time.Second)
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	allowed, remaining, _, err := rl.Allow(ctx, key, limit, 10*time.Second)
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

func TestRedis_SlidingWindow(t *testing.T) {
	rl := newTestRedisLimiter(t)
	ctx := context.Background()
	limit := 2
	window := 2 * time.Second
	key := "test:sliding:" + t.Name()

	// Exhaust limit.
	for i := 0; i < limit; i++ {
		_, _, _, _ = rl.Allow(ctx, key, limit, window)
	}

	// Should be rejected.
	allowed, _, _, _ := rl.Allow(ctx, key, limit, window)
	if allowed {
		t.Fatal("should be rejected")
	}

	// Wait for window to expire.
	time.Sleep(window + 200*time.Millisecond)

	// Should be allowed again.
	allowed, _, _, err := rl.Allow(ctx, key, limit, window)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed after window expiry")
	}
}

func TestRedis_LuaScript_Atomicity(t *testing.T) {
	rl := newTestRedisLimiter(t)
	ctx := context.Background()
	limit := 20
	goroutines := 50
	key := "test:atomic:" + t.Name()

	var allowedCount atomic.Int32
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			allowed, _, _, _ := rl.Allow(ctx, key, limit, 10*time.Second)
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

func TestRedis_KeyExpiry(t *testing.T) {
	rl := newTestRedisLimiter(t)
	ctx := context.Background()
	key := "test:expiry:" + t.Name()
	window := 1 * time.Second

	_, _, _, _ = rl.Allow(ctx, key, 10, window)

	// Wait for key to expire.
	time.Sleep(window + 500*time.Millisecond)

	// After expiry, should get fresh quota.
	allowed, remaining, _, err := rl.Allow(ctx, key, 10, window)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed after key expiry")
	}
	if remaining != 9 {
		t.Fatalf("expected remaining=9, got %d", remaining)
	}
}

func TestRedis_Ping(t *testing.T) {
	rl := newTestRedisLimiter(t)
	ctx := context.Background()

	if err := rl.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
}

func TestRedis_QueryTimeout(t *testing.T) {
	addr := redisAddr(t)
	rl := NewRedisLimiter(&config.RedisConfig{
		Addr:         addr,
		QueryTimeout: 1 * time.Nanosecond,
	})
	t.Cleanup(func() { _ = rl.Close() })

	_, _, _, err := rl.Allow(context.Background(), "test:timeout:"+t.Name(), 10, 10*time.Second)
	if err == nil {
		t.Fatal("expected error due to 1ns query timeout")
	}
}
