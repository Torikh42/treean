package redisstore

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var fixedWindowScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {current, ttl}
`)

// Allow checks and increments a fixed-window counter in Redis, failing open if Redis is unavailable or unconfigured.
func (c *Client) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	if !c.Enabled() || limit <= 0 || window <= 0 || strings.TrimSpace(key) == "" {
		return true, 0, nil
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	windowMs := max(window.Milliseconds(), int64(1))
	res, err := fixedWindowScript.Run(opCtx, c.rdb, []string{key}, windowMs).Slice()
	if err != nil {
		c.logDegraded(opCtx, "redis rate limit failed open", key, err)
		return true, 0, nil
	}
	if len(res) < 2 {
		c.logger.WarnContext(opCtx, "redis rate limit unexpected script response; failing open", slog.String("key", key))
		return true, 0, nil
	}

	count, okCount := toInt64(res[0])
	ttlMs, okTTL := toInt64(res[1])
	if !okCount {
		return true, 0, nil
	}
	if count <= int64(limit) {
		return true, 0, nil
	}

	retryAfter := window
	if okTTL && ttlMs > 0 {
		retryAfter = time.Duration(ttlMs) * time.Millisecond
	}
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return false, retryAfter, nil
}

// ConsumeOnce atomically marks key as used for ttl using Redis SET NX, failing open if Redis is unavailable or unconfigured.
func (c *Client) ConsumeOnce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if !c.Enabled() || strings.TrimSpace(key) == "" || ttl <= 0 {
		return true, nil
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	_, err := c.rdb.SetArgs(opCtx, key, "1", redis.SetArgs{
		Mode: "NX",
		TTL:  ttl,
	}).Result()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, redis.Nil) {
		return false, nil
	}

	c.logDegraded(opCtx, "redis consume-once failed open", key, err)
	return true, nil
}

// Peek reads the current fixed-window counter and remaining TTL without incrementing, failing open on Redis errors.
func (c *Client) Peek(ctx context.Context, key string, limit int) (bool, time.Duration, error) {
	if !c.Enabled() || limit <= 0 || strings.TrimSpace(key) == "" {
		return true, 0, nil
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	pipe := c.rdb.Pipeline()
	getCmd := pipe.Get(opCtx, key)
	pttlCmd := pipe.PTTL(opCtx, key)
	_, pipeErr := pipe.Exec(opCtx)

	valStr, err := getCmd.Result()
	if errors.Is(err, redis.Nil) {
		return true, 0, nil
	}
	if err != nil {
		c.logDegraded(opCtx, "redis rate limit peek failed open", key, err)
		return true, 0, nil
	}
	if pipeErr != nil && !errors.Is(pipeErr, redis.Nil) {
		if pttlErr := pttlCmd.Err(); pttlErr != nil {
			c.logDegraded(opCtx, "redis rate limit peek pttl failed open", key, pttlErr)
		}
	}

	count, parseErr := strconv.ParseInt(valStr, 10, 64)
	if parseErr != nil {
		c.logDegraded(opCtx, "redis rate limit peek parse failed open", key, parseErr)
		return true, 0, nil
	}
	if count < int64(limit) {
		return true, 0, nil
	}

	retryAfter := time.Second
	if ttl, ttlErr := pttlCmd.Result(); ttlErr == nil && ttl > time.Second {
		retryAfter = ttl
	}
	return false, retryAfter, nil
}

// Reset deletes a rate-limit counter key best-effort, failing open if Redis is unavailable or unconfigured.
func (c *Client) Reset(ctx context.Context, key string) error {
	if !c.Enabled() || strings.TrimSpace(key) == "" {
		return nil
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	if err := c.rdb.Del(opCtx, key).Err(); err != nil {
		c.logDegraded(opCtx, "redis rate limit reset failed open", key, err)
	}
	return nil
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}
