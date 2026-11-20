package redisstore

import (
	"context"
	"log/slog"
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
	if !c.Enabled() || limit <= 0 || window <= 0 || key == "" {
		return true, 0, nil
	}

	opCtx, cancel := c.withTimeout(ctx)
	defer cancel()

	windowMs := max(window.Milliseconds(), int64(1))
	res, err := fixedWindowScript.Run(opCtx, c.rdb, []string{key}, windowMs).Slice()
	if err != nil {
		c.logger.WarnContext(opCtx, "redis rate limit failed open",
			slog.String("key", key),
			slog.String("error", err.Error()),
		)
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
