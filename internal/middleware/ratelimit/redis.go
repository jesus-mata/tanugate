package ratelimit

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jesus-mata/tanugate/internal/config"
	"github.com/redis/go-redis/v9"
)

// memberCounter generates unique sorted-set members without crypto/rand overhead.
var memberCounter atomic.Uint64

// instanceID uniquely identifies this process so that sorted-set members
// cannot collide across gateway instances or restarts.
var instanceID = generateInstanceID()

func generateInstanceID() string {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("%d:%s", os.Getpid(), hex.EncodeToString(rnd[:]))
}

// slidingWindowScript is a Lua script implementing a sliding window rate
// limiter using a sorted set. It atomically trims expired entries, checks the
// count, and adds a new entry if under the limit.
var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, ARGV[4])
    redis.call('PEXPIRE', key, window)
    return {1, limit - count - 1, now + window}
else
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local reset = now + window
    if #oldest > 0 then reset = tonumber(oldest[2]) + window end
    return {0, 0, reset}
end
`)

// leakyBucketScript is a Lua script implementing a leaky bucket rate limiter
// using a Redis hash. The hash stores two fields: "level" (current water level
// as a float) and "last_update" (timestamp in milliseconds). Requests leak at
// a constant rate of capacity/window_ms. Each request adds 1 to the level if
// room exists, otherwise it is rejected.
var leakyBucketScript = redis.NewScript(`
local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local leak_rate = capacity / window_ms

local data = redis.call('HMGET', key, 'level', 'last_update')
local level = tonumber(data[1]) or 0
local last_update = tonumber(data[2]) or now_ms

local elapsed = now_ms - last_update
if elapsed > 0 then
    local leaked = elapsed * leak_rate
    level = math.max(level - leaked, 0)
end

if level + 1 <= capacity then
    level = level + 1
    redis.call('HSET', key, 'level', tostring(level), 'last_update', tostring(now_ms))
    redis.call('PEXPIRE', key, window_ms)
    local remaining = math.floor(capacity - level)
    local reset_ms = now_ms + math.ceil((level) / leak_rate)
    return {1, remaining, reset_ms}
else
    redis.call('HSET', key, 'level', tostring(level), 'last_update', tostring(now_ms))
    redis.call('PEXPIRE', key, window_ms)
    local reset_ms = now_ms + math.ceil(1 / leak_rate)
    return {0, 0, reset_ms}
end
`)

// RedisLimiter implements the Limiter interface using a Redis-backed sliding
// window algorithm.
type RedisLimiter struct {
	client       *redis.Client
	queryTimeout time.Duration
	now          func() time.Time
}

// NewRedisLimiter creates a RedisLimiter connected to the Redis instance
// described by cfg.
func NewRedisLimiter(cfg *config.RedisConfig) *RedisLimiter {
	opts := &redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		MaxRetries:   cfg.MaxRetries,
	}
	if cfg.TLSEnabled {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(opts)
	return &RedisLimiter{client: client, queryTimeout: cfg.QueryTimeout, now: time.Now}
}

// Allow checks whether a request identified by key should be allowed under the
// given limit and window. It dispatches to the appropriate algorithm
// implementation based on the algorithm parameter.
func (rl *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration, algorithm Algorithm) (bool, int, time.Time, error) {
	if rl.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rl.queryTimeout)
		defer cancel()
	}

	nowMs := rl.now().UnixMilli()
	windowMs := window.Milliseconds()

	switch algorithm {
	case AlgorithmLeakyBucket:
		return rl.allowLeakyBucket(ctx, key, limit, windowMs, nowMs)
	default:
		return rl.allowSlidingWindow(ctx, key, limit, windowMs, nowMs)
	}
}

func (rl *RedisLimiter) allowSlidingWindow(ctx context.Context, key string, limit int, windowMs, nowMs int64) (bool, int, time.Time, error) {
	member := instanceID + ":" + strconv.FormatInt(nowMs, 10) + ":" + strconv.FormatUint(memberCounter.Add(1), 10)
	result, err := slidingWindowScript.Run(ctx, rl.client, []string{key}, windowMs, limit, nowMs, member).Int64Slice()
	if err != nil {
		return false, 0, time.Time{}, err
	}

	allowed := result[0] == 1
	remaining := int(result[1])
	resetAt := time.UnixMilli(result[2])

	return allowed, remaining, resetAt, nil
}

func (rl *RedisLimiter) allowLeakyBucket(ctx context.Context, key string, capacity int, windowMs, nowMs int64) (bool, int, time.Time, error) {
	result, err := leakyBucketScript.Run(ctx, rl.client, []string{key}, capacity, windowMs, nowMs).Int64Slice()
	if err != nil {
		return false, 0, time.Time{}, err
	}

	allowed := result[0] == 1
	remaining := int(result[1])
	resetAt := time.UnixMilli(result[2])

	return allowed, remaining, resetAt, nil
}

// HealthCheck pings Redis to verify connectivity.
func (rl *RedisLimiter) HealthCheck(ctx context.Context) error {
	return rl.client.Ping(ctx).Err()
}

// Close closes the Redis client connection.
func (rl *RedisLimiter) Close() error {
	return rl.client.Close()
}
