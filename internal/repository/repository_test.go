package repository

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/torikh42/treean/internal/config"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/migrations"
)

type failingDirFS struct {
	err error
}

func (f failingDirFS) Open(string) (fs.File, error) {
	return nil, f.err
}

func (f failingDirFS) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, f.err
}

func TestCollectMigrationFiles(t *testing.T) {
	t.Parallel()

	t.Run("sorts sql files lexically and skips non-sql entries and dirs", func(t *testing.T) {
		t.Parallel()

		fsys := fstest.MapFS{
			"003_indexes.sql":       {Data: []byte("SELECT 3;")},
			"001_init.sql":          {Data: []byte("SELECT 1;")},
			"002_seed.sql":          {Data: []byte("SELECT 2;")},
			"embed.go":              {Data: []byte("package migrations")},
			"README.md":             {Data: []byte("# docs")},
			"004_notes.sql.bak":     {Data: []byte("backup")},
			"nested/999_ignore.sql": {Data: []byte("SELECT 999;")},
		}

		got, err := collectMigrationFiles(fsys)
		if err != nil {
			t.Fatalf("collectMigrationFiles() error = %v", err)
		}

		want := []string{"001_init.sql", "002_seed.sql", "003_indexes.sql"}
		if !slices.Equal(got, want) {
			t.Fatalf("collectMigrationFiles() = %v, want %v", got, want)
		}
	})

	t.Run("empty fs returns empty slice", func(t *testing.T) {
		t.Parallel()

		got, err := collectMigrationFiles(fstest.MapFS{})
		if err != nil {
			t.Fatalf("collectMigrationFiles() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("collectMigrationFiles() = %v, want empty", got)
		}
	})

	t.Run("embedded migrations fs lists ordered sql files", func(t *testing.T) {
		t.Parallel()

		got, err := collectMigrationFiles(migrations.Files)
		if err != nil {
			t.Fatalf("collectMigrationFiles(migrations.Files) error = %v", err)
		}

		want := []string{"001_init_schema.sql", "002_seed_demo.sql"}
		if !slices.Equal(got, want) {
			t.Fatalf("collectMigrationFiles(migrations.Files) = %v, want %v", got, want)
		}
	})

	t.Run("nil fs returns error", func(t *testing.T) {
		t.Parallel()

		if _, err := collectMigrationFiles(nil); err == nil {
			t.Fatal("expected error for nil filesystem")
		}
	})

	t.Run("propagates readdir error", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("read dir failure")
		_, err := collectMigrationFiles(failingDirFS{err: boom})
		if !errors.Is(err, boom) {
			t.Fatalf("collectMigrationFiles() error = %v, want errors.Is(_, %v)", err, boom)
		}
	})
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "trims and lowercases mixed case", in: "  Buyer1@Event.LOCAL ", want: "buyer1@event.local"},
		{name: "lowercases uppercase", in: "ORGANIZER@EVENT.LOCAL", want: "organizer@event.local"},
		{name: "empty string", in: "", want: ""},
		{name: "whitespace only", in: "  \t\n ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeEmail(tc.in); got != tc.want {
				t.Fatalf("normalizeEmail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapUserCreateError(t *testing.T) {
	t.Parallel()

	if err := mapUserCreateError(nil); err != nil {
		t.Fatalf("mapUserCreateError(nil) = %v, want nil", err)
	}

	t.Run("users_email_key unique violation returns EMAIL_ALREADY_EXISTS conflict", func(t *testing.T) {
		t.Parallel()

		pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}
		for _, tc := range []struct {
			name string
			err  error
		}{
			{name: "direct", err: pgErr},
			{name: "wrapped", err: fmt.Errorf("tx failed: %w", pgErr)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got := mapUserCreateError(tc.err)
				de, ok := errors.AsType[*domain.DomainError](got)
				if !ok {
					t.Fatalf("expected *domain.DomainError, got %T (%v)", got, got)
				}
				if de.HTTPStatus != http.StatusConflict || de.Code != domain.ErrCodeEmailAlreadyExists {
					t.Fatalf("got (%d, %q), want (%d, %q)", de.HTTPStatus, de.Code, http.StatusConflict, domain.ErrCodeEmailAlreadyExists)
				}
			})
		}
	})

	t.Run("users_pkey or unknown unique constraint is wrapped without domain mapping", func(t *testing.T) {
		t.Parallel()

		for _, constraint := range []string{"users_pkey", ""} {
			pgErr := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
			got := mapUserCreateError(pgErr)
			if _, ok := errors.AsType[*domain.DomainError](got); ok {
				t.Fatalf("constraint %q unexpectedly mapped to *domain.DomainError: %v", constraint, got)
			}
			if parsed, ok := errors.AsType[*pgconn.PgError](got); !ok || parsed.Code != "23505" {
				t.Fatalf("expected wrapped *pgconn.PgError 23505 for %q, got %v", constraint, got)
			}
		}
	})

	t.Run("non-unique pg error is wrapped without domain mapping", func(t *testing.T) {
		t.Parallel()

		pgErr := &pgconn.PgError{Code: "23503"}
		got := mapUserCreateError(pgErr)
		if _, ok := errors.AsType[*domain.DomainError](got); ok {
			t.Fatalf("unexpected *domain.DomainError for non-23505: %v", got)
		}
		if parsed, ok := errors.AsType[*pgconn.PgError](got); !ok || parsed.Code != "23503" {
			t.Fatalf("expected wrapped *pgconn.PgError 23503, got %v", got)
		}
	})

	t.Run("generic error is wrapped", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("connection reset")
		got := mapUserCreateError(sentinel)
		if !errors.Is(got, sentinel) {
			t.Fatalf("expected wrapped sentinel error, got %v", got)
		}
	})
}

func TestMapEventCreateError(t *testing.T) {
	t.Parallel()

	if err := mapEventCreateError(nil); err != nil {
		t.Fatalf("mapEventCreateError(nil) = %v, want nil", err)
	}

	t.Run("organizer foreign key violation returns UNAUTHORIZED 401", func(t *testing.T) {
		t.Parallel()

		pgErr := &pgconn.PgError{Code: "23503", ConstraintName: "events_organizer_id_fkey"}
		for _, tc := range []struct {
			name string
			err  error
		}{
			{name: "direct", err: pgErr},
			{name: "wrapped", err: fmt.Errorf("tx failed: %w", pgErr)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got := mapEventCreateError(tc.err)
				de, ok := errors.AsType[*domain.DomainError](got)
				if !ok {
					t.Fatalf("expected *domain.DomainError, got %T (%v)", got, got)
				}
				if de.HTTPStatus != http.StatusUnauthorized || de.Code != domain.ErrCodeUnauthorized {
					t.Fatalf("got (%d, %q), want (%d, %q)", de.HTTPStatus, de.Code, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
				}
			})
		}
	})

	t.Run("other pg errors are wrapped without domain mapping", func(t *testing.T) {
		t.Parallel()

		for _, pgErr := range []*pgconn.PgError{
			{Code: "23503", ConstraintName: "other_fkey"},
			{Code: "23505", ConstraintName: "events_pkey"},
		} {
			got := mapEventCreateError(pgErr)
			if _, ok := errors.AsType[*domain.DomainError](got); ok {
				t.Fatalf("unexpected *domain.DomainError for %+v: %v", pgErr, got)
			}
			if parsed, ok := errors.AsType[*pgconn.PgError](got); !ok || parsed.Code != pgErr.Code {
				t.Fatalf("expected wrapped *pgconn.PgError %s, got %v", pgErr.Code, got)
			}
		}
	})

	t.Run("generic error is wrapped", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("insert event failed")
		got := mapEventCreateError(sentinel)
		if !errors.Is(got, sentinel) {
			t.Fatalf("expected wrapped sentinel error, got %v", got)
		}
	})
}

func TestMapTicketTypeCreateError(t *testing.T) {
	t.Parallel()

	if err := mapTicketTypeCreateError(nil); err != nil {
		t.Fatalf("mapTicketTypeCreateError(nil) = %v, want nil", err)
	}

	t.Run("tier name unique constraints return VALIDATION_ERROR 422", func(t *testing.T) {
		t.Parallel()

		for _, constraint := range []string{"ticket_types_event_id_name_key", "uidx_ticket_types_event_lower_name"} {
			pgErr := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
			for _, tc := range []struct {
				name string
				err  error
			}{
				{name: constraint + "/direct", err: pgErr},
				{name: constraint + "/wrapped", err: fmt.Errorf("tx failed: %w", pgErr)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					got := mapTicketTypeCreateError(tc.err)
					de, ok := errors.AsType[*domain.DomainError](got)
					if !ok {
						t.Fatalf("expected *domain.DomainError, got %T (%v)", got, got)
					}
					if de.HTTPStatus != http.StatusUnprocessableEntity || de.Code != domain.ErrCodeValidation {
						t.Fatalf("got (%d, %q), want (%d, %q)", de.HTTPStatus, de.Code, http.StatusUnprocessableEntity, domain.ErrCodeValidation)
					}
				})
			}
		}
	})

	t.Run("ticket_types_pkey or unknown unique constraint is wrapped without domain mapping", func(t *testing.T) {
		t.Parallel()

		for _, constraint := range []string{"ticket_types_pkey", ""} {
			pgErr := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
			got := mapTicketTypeCreateError(pgErr)
			if _, ok := errors.AsType[*domain.DomainError](got); ok {
				t.Fatalf("constraint %q unexpectedly mapped to *domain.DomainError: %v", constraint, got)
			}
			if parsed, ok := errors.AsType[*pgconn.PgError](got); !ok || parsed.Code != "23505" {
				t.Fatalf("expected wrapped *pgconn.PgError 23505 for %q, got %v", constraint, got)
			}
		}
	})

	t.Run("non-unique pg error is wrapped without domain mapping", func(t *testing.T) {
		t.Parallel()

		pgErr := &pgconn.PgError{Code: "23514"}
		got := mapTicketTypeCreateError(pgErr)
		if _, ok := errors.AsType[*domain.DomainError](got); ok {
			t.Fatalf("unexpected *domain.DomainError for non-23505: %v", got)
		}
		if parsed, ok := errors.AsType[*pgconn.PgError](got); !ok || parsed.Code != "23514" {
			t.Fatalf("expected wrapped *pgconn.PgError 23514, got %v", got)
		}
	})

	t.Run("generic error is wrapped", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("tx aborted")
		got := mapTicketTypeCreateError(sentinel)
		if !errors.Is(got, sentinel) {
			t.Fatalf("expected wrapped sentinel error, got %v", got)
		}
	})
}

func TestEnsureDemoUser_Validation(t *testing.T) {
	t.Parallel()

	repo := &UserRepo{}
	ctx := context.Background()

	t.Run("rejects nil uuid before touching pool", func(t *testing.T) {
		t.Parallel()

		if err := repo.EnsureDemoUser(ctx, uuid.Nil, domain.RoleBuyer); err == nil {
			t.Fatal("expected error for uuid.Nil")
		}
	})

	t.Run("rejects invalid roles before touching pool", func(t *testing.T) {
		t.Parallel()

		for _, role := range []domain.Role{"", domain.Role("ADMIN"), domain.Role("buyer")} {
			if err := repo.EnsureDemoUser(ctx, uuid.New(), role); err == nil {
				t.Fatalf("expected error for invalid role %q", role)
			}
		}
	})

	t.Run("accepts valid demo user inputs", func(t *testing.T) {
		t.Parallel()

		for _, role := range []domain.Role{domain.RoleOrganizer, domain.RoleBuyer} {
			if err := validateDemoUserInput(uuid.New(), role); err != nil {
				t.Fatalf("validateDemoUserInput(_, %q) unexpected error = %v", role, err)
			}
		}
	})
}

func TestApplyMigrations_Guards(t *testing.T) {
	t.Parallel()

	t.Run("nil pool returns error", func(t *testing.T) {
		t.Parallel()

		if err := ApplyMigrations(context.Background(), nil); err == nil {
			t.Fatal("expected error when pool is nil")
		}
	})

	t.Run("nil context returns error", func(t *testing.T) {
		t.Parallel()

		var nilCtx context.Context
		if err := ApplyMigrations(nilCtx, nil); err == nil {
			t.Fatal("expected error when context is nil")
		}
	})
}

func TestNewPool_GuardsAndMalformedDSN(t *testing.T) {
	t.Parallel()

	const secretPassword = "super_secret_pw"

	t.Run("nil context returns error", func(t *testing.T) {
		t.Parallel()

		var nilCtx context.Context
		pool, err := NewPool(nilCtx, config.Config{
			DatabaseURL: "postgres://u:p@localhost:5432/ticketing?sslmode=disable",
			DBMaxConns:  10,
			DBMinConns:  1,
		})
		if err == nil || pool != nil {
			if pool != nil {
				pool.Close()
			}
			t.Fatalf("expected nil pool and error for nil context, got pool=%v err=%v", pool, err)
		}
	})

	t.Run("canceled context does not leak secret password", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		pool, err := NewPool(ctx, config.Config{
			DatabaseURL: "postgres://user:" + secretPassword + "@127.0.0.1:1/db?sslmode=disable",
			DBMaxConns:  5,
			DBMinConns:  1,
		})
		if err == nil {
			if pool != nil {
				pool.Close()
			}
			t.Fatal("expected error for canceled context")
		}
		if pool != nil {
			pool.Close()
			t.Fatalf("expected nil pool on error, got %v", pool)
		}
		if strings.Contains(err.Error(), secretPassword) {
			t.Fatalf("NewPool error leaked DSN password: %v", err)
		}
	})

	cases := []struct {
		name string
		dsn  string
	}{
		{name: "empty dsn", dsn: ""},
		{name: "whitespace dsn", dsn: "   \t\n  "},
		{name: "missing scheme before colon slash slash", dsn: "://bad-dsn"},
		{name: "invalid percent escape with password", dsn: "postgres://user:" + secretPassword + "@%zz:5432/db"},
		{name: "unclosed ipv6 bracket with password", dsn: "postgres://user:" + secretPassword + "@[::1:5432/db"},
		{name: "invalid pool max conns param with password", dsn: "postgres://user:" + secretPassword + "@localhost:5432/db?pool_max_conns=notanint"},
		{name: "unterminated quote in keyword dsn", dsn: "host=localhost user=app password='" + secretPassword},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			pool, err := NewPool(ctx, config.Config{
				DatabaseURL: tc.dsn,
				DBMaxConns:  50,
				DBMinConns:  5,
			})
			if err == nil {
				if pool != nil {
					pool.Close()
				}
				t.Fatalf("NewPool(%q) succeeded, expected fast parse error", tc.dsn)
			}
			if pool != nil {
				pool.Close()
				t.Fatalf("expected nil pool on error, got %v", pool)
			}
			if strings.Contains(err.Error(), secretPassword) {
				t.Fatalf("NewPool error leaked DSN password: %v", err)
			}
		})
	}
}

func TestBuildPoolConfig(t *testing.T) {
	t.Parallel()

	t.Run("valid pool bounds and timeouts", func(t *testing.T) {
		t.Parallel()

		cfg := config.Config{
			DatabaseURL: "postgres://u:p@localhost:5432/ticketing?sslmode=disable",
			DBMaxConns:  42,
			DBMinConns:  7,
		}

		poolCfg, err := buildPoolConfig(cfg)
		if err != nil {
			t.Fatalf("buildPoolConfig() error = %v", err)
		}
		if poolCfg.MaxConns != 42 {
			t.Fatalf("MaxConns = %d, want 42", poolCfg.MaxConns)
		}
		if poolCfg.MinConns != 7 {
			t.Fatalf("MinConns = %d, want 7", poolCfg.MinConns)
		}
		if poolCfg.MaxConnLifetime != 30*time.Minute {
			t.Fatalf("MaxConnLifetime = %v, want 30m", poolCfg.MaxConnLifetime)
		}
		if poolCfg.MaxConnIdleTime != 5*time.Minute {
			t.Fatalf("MaxConnIdleTime = %v, want 5m", poolCfg.MaxConnIdleTime)
		}
	})

	t.Run("rejects invalid pool bounds", func(t *testing.T) {
		t.Parallel()

		baseURL := "postgres://u:p@localhost:5432/ticketing?sslmode=disable"
		cases := []struct {
			name string
			cfg  config.Config
		}{
			{name: "zero max conns", cfg: config.Config{DatabaseURL: baseURL, DBMaxConns: 0, DBMinConns: 0}},
			{name: "negative max conns", cfg: config.Config{DatabaseURL: baseURL, DBMaxConns: -1, DBMinConns: 0}},
			{name: "negative min conns", cfg: config.Config{DatabaseURL: baseURL, DBMaxConns: 10, DBMinConns: -1}},
			{name: "min conns exceeds max conns", cfg: config.Config{DatabaseURL: baseURL, DBMaxConns: 5, DBMinConns: 10}},
			{name: "blank database url", cfg: config.Config{DatabaseURL: "   ", DBMaxConns: 10, DBMinConns: 1}},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, err := buildPoolConfig(tc.cfg); err == nil {
					t.Fatalf("buildPoolConfig(%+v) succeeded, want error", tc.cfg)
				}
			})
		}
	})
}
