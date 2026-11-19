package repository

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/migrations"
)

const migrationLockID int64 = 74839201

// ApplyMigrations acquires a PostgreSQL advisory lock and applies embedded SQL migrations once in lexical order.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool) (retErr error) {
	if ctx == nil {
		return errors.New("context is required")
	}
	if pool == nil {
		return errors.New("database pool is required")
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquiring migration advisory lock: %w", err)
	}
	// Deferred after conn.Release so LIFO ordering unlocks or closes the session before returning to pgxpool.
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), txCleanupGrace)
		defer cancel()

		var unlocked bool
		unlockErr := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID).Scan(&unlocked)
		if unlockErr != nil || !unlocked {
			// Destroy the underlying connection so a still-locked session is never recycled cleanly.
			_ = conn.Conn().Close(unlockCtx)
			if unlockErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("releasing migration advisory lock: %w", unlockErr))
			} else {
				retErr = errors.Join(retErr, errors.New("releasing migration advisory lock: lock was not held"))
			}
		}
	}()

	const createTableSQL = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`
	if _, err := conn.Exec(ctx, createTableSQL); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	files, err := collectMigrationFiles(migrations.Files)
	if err != nil {
		return err
	}

	for _, name := range files {
		if err := applyMigrationFile(ctx, conn, migrations.Files, name); err != nil {
			return err
		}
	}

	return nil
}

// collectMigrationFiles returns sorted *.sql filenames at the root of fsys.
func collectMigrationFiles(fsys fs.FS) ([]string, error) {
	if fsys == nil {
		return nil, errors.New("reading embedded migrations: nil filesystem")
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	slices.Sort(files)
	return files, nil
}

// applyMigrationFile checks whether name was already applied and executes it inside a transaction otherwise.
func applyMigrationFile(ctx context.Context, conn *pgxpool.Conn, fsys fs.FS, name string) error {
	var alreadyApplied bool
	err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)", name).Scan(&alreadyApplied)
	if err != nil {
		return fmt.Errorf("checking migration %s: %w", name, err)
	}
	if alreadyApplied {
		return nil
	}

	sqlBytes, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("reading migration %s: %w", name, err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning tx for migration %s: %w", name, err)
	}
	defer rollbackTx(tx)

	if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("executing migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
		return fmt.Errorf("recording migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing migration %s: %w", name, err)
	}
	return nil
}
