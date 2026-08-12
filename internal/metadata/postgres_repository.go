// Package metadata's Postgres implementation is split across several files
// by domain, all sharing the same *PostgresRepository struct and connection
// pool:
//
//	postgres_repository.go               - construction, migrations, shared helpers (this file)
//	postgres_repository_files.go         - FileRepository (the `files` table)
//	postgres_repository_nodestatus.go    - NodeStatusRepository (the `node_status` table)
//	postgres_repository_admin.go         - AdminRepository (the `admins` / `admin_sessions` tables)
//	postgres_repository_apikeys.go       - APIKeyRepository (the `api_keys` table)
//	postgres_repository_uploadsettings.go - UploadSettingsRepository (the `upload_settings` table)
//
// Adding a new domain: create postgres_repository_<domain>.go implementing
// your new interface as methods on *PostgresRepository, add a migration
// under postgres_migrations/, and embed your interface into Repository in
// repository.go. See CONTRIBUTING.md "Adding a new resource" for the full
// walkthrough.
package metadata

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed postgres_migrations
var postgresMigrationFiles embed.FS

const postgresMigrationLockID = 72186_004

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(ctx context.Context, dsn string, maxConns int32) (*PostgresRepository, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}

	poolCfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) Migrate(ctx context.Context) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, postgresMigrationLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, postgresMigrationLockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    TEXT PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	entries, err := postgresMigrationFiles.ReadDir("postgres_migrations")
	if err != nil {
		return fmt.Errorf("read postgres migrations directory: %w", err)
	}

	var filenames []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filenames = append(filenames, entry.Name())
	}
	sort.Strings(filenames)

	for _, filename := range filenames {
		var alreadyApplied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`,
			filename,
		).Scan(&alreadyApplied); err != nil {
			return fmt.Errorf("check migration status for %s: %w", filename, err)
		}
		if alreadyApplied {
			continue
		}

		contents, err := postgresMigrationFiles.ReadFile("postgres_migrations/" + filename)
		if err != nil {
			return fmt.Errorf("read migration file %s: %w", filename, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction for migration %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, filename,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", filename, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", filename, err)
		}
	}

	return nil
}

func (r *PostgresRepository) Close() error {
	r.pool.Close()
	return nil
}

// pgRowScanner is satisfied by both pgx.Row and pgx.Rows, letting the
// per-domain scan helpers work for both QueryRow and Query call sites.
type pgRowScanner interface {
	Scan(dest ...interface{}) error
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}


