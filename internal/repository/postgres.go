// Package repository implements PostgreSQL persistence using pgxpool and explicit transactions.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/config"
)

const (
	poolMaxConnLifetime = 30 * time.Minute
	poolMaxConnIdleTime = 5 * time.Minute
	txCleanupGrace      = 5 * time.Second
)

// NewPool configures and validates a PostgreSQL connection pool.
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}

	poolCfg, err := buildPoolConfig(cfg)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("connecting database: %w", err)
	}

	password := poolCfg.ConnConfig.Password
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, sanitizePoolError("creating pgxpool", err, password)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, sanitizePoolError("pinging database", err, password)
	}

	return pool, nil
}

// buildPoolConfig parses the DSN and applies pool bounds without exposing raw connection strings on error.
func buildPoolConfig(cfg config.Config) (*pgxpool.Config, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return nil, errors.New("parsing database url: empty database url")
	}
	if cfg.DBMaxConns <= 0 {
		return nil, errors.New("database max conns must be positive")
	}
	if cfg.DBMinConns < 0 {
		return nil, errors.New("database min conns must be non-negative")
	}
	if cfg.DBMinConns > cfg.DBMaxConns {
		return nil, fmt.Errorf("database min conns (%d) cannot exceed max conns (%d)", cfg.DBMinConns, cfg.DBMaxConns)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		// Return a static message because ambiguous malformed DSNs can bypass best-effort password redaction.
		return nil, errors.New("parsing database url: invalid database connection string")
	}

	poolCfg.MaxConns = cfg.DBMaxConns
	poolCfg.MinConns = cfg.DBMinConns
	poolCfg.MaxConnLifetime = poolMaxConnLifetime
	poolCfg.MaxConnIdleTime = poolMaxConnIdleTime
	return poolCfg, nil
}

// sanitizePoolError prevents parsed DSN secrets from surfacing in pool initialization or ping failures.
func sanitizePoolError(op string, err error, password string) error {
	if password != "" && strings.Contains(err.Error(), password) {
		return fmt.Errorf("%s: database connection error (details redacted)", op)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// rollbackTx rolls back uncommitted transactions using an independent bounded context.
func rollbackTx(tx pgx.Tx) {
	rbCtx, cancel := context.WithTimeout(context.Background(), txCleanupGrace)
	defer cancel()
	_ = tx.Rollback(rbCtx)
}
