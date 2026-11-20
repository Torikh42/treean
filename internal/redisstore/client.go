// Package redisstore provides fail-open Redis rate limiting and short-TTL event catalog caching.
package redisstore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultOperationTimeout = 500 * time.Millisecond

// Client wraps go-redis with strict operation timeouts and fail-open helpers.
type Client struct {
	rdb     *redis.Client
	timeout time.Duration
	logger  *slog.Logger
}

// NewClient creates a Redis client from redisURL. An empty URL disables Redis cleanly by returning (nil, nil).
func NewClient(redisURL string, timeout time.Duration, logger *slog.Logger) (*Client, error) {
	trimmed := strings.TrimSpace(redisURL)
	if trimmed == "" {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = defaultOperationTimeout
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	opts, err := redis.ParseURL(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parsing redis URL: %w", err)
	}
	opts.DialTimeout = timeout
	opts.ReadTimeout = timeout
	opts.WriteTimeout = timeout

	return &Client{
		rdb:     redis.NewClient(opts),
		timeout: timeout,
		logger:  logger,
	}, nil
}

// NewClientFromGoRedis wraps an already configured *redis.Client (useful for tests).
func NewClientFromGoRedis(rdb *redis.Client, timeout time.Duration, logger *slog.Logger) *Client {
	if rdb == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultOperationTimeout
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Client{
		rdb:     rdb,
		timeout: timeout,
		logger:  logger,
	}
}

// Enabled reports whether a Redis backend is configured.
func (c *Client) Enabled() bool {
	return c != nil && c.rdb != nil
}

// Close releases underlying Redis pool resources cleanly.
func (c *Client) Close() error {
	if !c.Enabled() {
		return nil
	}
	return c.rdb.Close()
}

func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, c.timeout)
}
