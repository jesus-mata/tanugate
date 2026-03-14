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

// RedisLimiter implements the Limiter interface using a Redis-backed sliding
// window algorithm.
type RedisLimiter struct {
	client       *redis.Client
	queryTimeout time.Duration
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
	return &RedisLimiter{client: client, queryTimeout: cfg.QueryTimeout}
}

// Allow checks whether a request identified by key should be allowed under the
// given limit and window using a Redis sliding window.
func (rl *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, time.Time, error) {
	if rl.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rl.queryTimeout)
		defer cancel()
	}

	nowMs := time.Now().UnixMilli()
	windowMs := window.Milliseconds()

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

// HealthCheck pings Redis to verify connectivity.
func (rl *RedisLimiter) HealthCheck(ctx context.Context) error {
	return rl.client.Ping(ctx).Err()
}

// Close closes the Redis client connection.
func (rl *RedisLimiter) Close() error {
	return rl.client.Close()
}
