package metadata

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed postgres_migrations/*.sql
var postgresMigrationFiles embed.FS

// postgresMigrationLockID is an arbitrary, fixed advisory lock key used to
// serialize Migrate() across every server instance that starts up against
// the same database at the same time (srv1/srv2/srv3 booting together,
// e.g. on a fresh deploy). Without this, two instances could both see a
// migration as "not yet applied" and race to run it. The value itself has
// no meaning beyond being a stable constant unique to this application's
// migration process.
const postgresMigrationLockID = 72186_004

// PostgresRepository is the sole Repository implementation, backed by
// Postgres. It supports both a single standalone instance and deployments
// that run more than one tempcdn instance (e.g. srv1/srv2/srv3 behind a
// rotating/round-robin frontend) against a single shared database, so
// metadata written by one instance is immediately visible to the others.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(ctx context.Context, dsn string) (*PostgresRepository, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
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

// Migrate applies any postgres_migrations/*.sql files not yet recorded in
// schema_migrations, in filename order, inside a single advisory lock so
// that multiple instances starting concurrently against the same database
// don't apply the same migration twice or race on schema_migrations itself.
func (r *PostgresRepository) Migrate(ctx context.Context) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration: %w", err)
	}
	defer conn.Release()

	// pg_advisory_lock blocks until held, and is automatically released
	// when the session ends or pg_advisory_unlock is called - using a
	// session-level (not transaction-level) lock here is deliberate, so it
	// stays held across the multiple statements below.
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

func (r *PostgresRepository) Insert(ctx context.Context, record *FileRecord) error {
	const query = `
		INSERT INTO files (
			id, original_name, content_type, size_bytes, checksum_sha256,
			object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err := r.pool.Exec(ctx, query,
		record.ID,
		record.OriginalName,
		record.ContentType,
		record.SizeBytes,
		record.ChecksumSHA256,
		record.ObjectKey,
		record.CDNURL,
		record.UploaderIPHash,
		record.DeleteTokenHash,
		record.CreatedAt,
		record.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert file record: %w", err)
	}
	return nil
}

func (r *PostgresRepository) FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*FileRecord, error) {
	const query = `
		SELECT id, original_name, content_type, size_bytes, checksum_sha256,
		       object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		FROM files
		WHERE checksum_sha256 = $1 AND expires_at > $2
		ORDER BY created_at DESC
		LIMIT 1
	`
	row := r.pool.QueryRow(ctx, query, checksum, now)
	return pgScanFileRecord(row)
}

func (r *PostgresRepository) FindByID(ctx context.Context, id string) (*FileRecord, error) {
	const query = `
		SELECT id, original_name, content_type, size_bytes, checksum_sha256,
		       object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		FROM files
		WHERE id = $1
	`
	row := r.pool.QueryRow(ctx, query, id)
	return pgScanFileRecord(row)
}

// FindExpired returns up to limit expired records, for the background
// expiry sweep. Because more than one instance (srv1/srv2/srv3) can run
// this concurrently against the shared database, the select uses FOR
// UPDATE SKIP LOCKED inside a transaction so that a row already claimed by
// one instance's sweep tick is invisible to another instance's concurrent
// sweep tick, rather than both instances trying to delete the same R2
// object and DB row. The transaction is committed (not just read) before
// returning, releasing the row locks once the caller has the records in
// hand; the caller (sweeper.sweepOnce) is responsible for actually calling
// DeleteByID once the R2 object is confirmed deleted.
func (r *PostgresRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*FileRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin find-expired transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op if already committed

	const query = `
		SELECT id, original_name, content_type, size_bytes, checksum_sha256,
		       object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		FROM files
		WHERE expires_at <= $1
		ORDER BY expires_at ASC
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(ctx, query, before, limit)
	if err != nil {
		return nil, fmt.Errorf("query expired file records: %w", err)
	}

	var records []*FileRecord
	for rows.Next() {
		record, err := pgScanFileRecordRows(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate expired file records: %w", err)
	}
	rows.Close()

	// Commit releases the row locks taken by FOR UPDATE. Rows returned here
	// remain "claimed" only in the sense that no other transaction could
	// see them as available during this query; once committed, another
	// sweeper's *next* tick could theoretically re-select the same row if
	// this instance fails to delete it in time - that's fine, since
	// DeleteByID is idempotent (ErrFileNotFound is treated as success by
	// the sweeper) and this only affects convergence speed, not
	// correctness.
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit find-expired transaction: %w", err)
	}

	return records, nil
}

func (r *PostgresRepository) Stats(ctx context.Context, now time.Time) (*Stats, error) {
	stats := &Stats{ContentTypeBreakdown: make(map[string]int64)}

	summaryRow := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM files
		WHERE expires_at > $1
	`, now)
	if err := summaryRow.Scan(&stats.ActiveFileCount, &stats.ActiveBytes); err != nil {
		return nil, fmt.Errorf("scan active file summary: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT content_type, COUNT(*)
		FROM files
		WHERE expires_at > $1
		GROUP BY content_type
	`, now)
	if err != nil {
		return nil, fmt.Errorf("query content type breakdown: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentType string
		var count int64
		if err := rows.Scan(&contentType, &count); err != nil {
			return nil, fmt.Errorf("scan content type breakdown row: %w", err)
		}
		stats.ContentTypeBreakdown[topLevelMimeType(contentType)] += count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate content type breakdown: %w", err)
	}

	return stats, nil
}

func (r *PostgresRepository) DeleteByID(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete file record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFileNotFound
	}
	return nil
}

// Heartbeat upserts this node's own row via INSERT ... ON CONFLICT, which
// Postgres executes atomically - safe against another instance's
// concurrent Heartbeat or MarkStaleOffline touching the same row, unlike a
// separate SELECT-then-INSERT/UPDATE would be.
func (r *PostgresRepository) Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error {
	const query = `
		INSERT INTO node_status (node_id, hostname, status, started_at, last_heartbeat_at, marked_offline_at)
		VALUES ($1, $2, 'online', $3, $4, NULL)
		ON CONFLICT (node_id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			status = 'online',
			last_heartbeat_at = EXCLUDED.last_heartbeat_at,
			marked_offline_at = NULL
	`
	_, err := r.pool.Exec(ctx, query, nodeID, hostname, startedAt, now)
	if err != nil {
		return fmt.Errorf("upsert node heartbeat: %w", err)
	}
	return nil
}

// MarkStaleOffline flips stale "online" rows to "offline" in one statement.
// The WHERE status = 'online' guard, combined with Postgres's row-level
// locking during the UPDATE, means that if two instances' janitor ticks
// somehow overlap on the same row, only one of them actually performs the
// flip - the other's UPDATE simply matches zero rows for that node_id, it
// does not error or double-flip.
func (r *PostgresRepository) MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error) {
	const query = `
		UPDATE node_status
		SET status = 'offline', marked_offline_at = $2
		WHERE status = 'online' AND last_heartbeat_at <= $1
		RETURNING node_id
	`
	rows, err := r.pool.Query(ctx, query, before, now)
	if err != nil {
		return nil, fmt.Errorf("mark stale nodes offline: %w", err)
	}
	defer rows.Close()

	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("scan marked-offline node id: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate marked-offline node ids: %w", err)
	}
	return nodeIDs, nil
}

func (r *PostgresRepository) ListNodeStatus(ctx context.Context) ([]*NodeStatus, error) {
	const query = `
		SELECT node_id, hostname, status, started_at, last_heartbeat_at, marked_offline_at
		FROM node_status
		ORDER BY last_heartbeat_at DESC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query node status: %w", err)
	}
	defer rows.Close()

	var nodes []*NodeStatus
	for rows.Next() {
		node, err := pgScanNodeStatusRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node status row: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node status rows: %w", err)
	}
	return nodes, nil
}

func (r *PostgresRepository) Close() error {
	r.pool.Close()
	return nil
}

// pgRowScanner is satisfied by both pgx.Row and pgx.Rows.
type pgRowScanner interface {
	Scan(dest ...interface{}) error
}

func pgScanFileRecord(row pgx.Row) (*FileRecord, error) {
	record, err := pgScanFileRecordRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan file record: %w", err)
	}
	return record, nil
}

func pgScanFileRecordRow(scanner pgRowScanner) (*FileRecord, error) {
	var record FileRecord
	err := scanner.Scan(
		&record.ID,
		&record.OriginalName,
		&record.ContentType,
		&record.SizeBytes,
		&record.ChecksumSHA256,
		&record.ObjectKey,
		&record.CDNURL,
		&record.UploaderIPHash,
		&record.DeleteTokenHash,
		&record.CreatedAt,
		&record.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func pgScanFileRecordRows(rows pgx.Rows) (*FileRecord, error) {
	record, err := pgScanFileRecordRow(rows)
	if err != nil {
		return nil, fmt.Errorf("scan file record row: %w", err)
	}
	return record, nil
}

func pgScanNodeStatusRow(scanner pgRowScanner) (*NodeStatus, error) {
	var node NodeStatus
	var markedOfflineAt *time.Time
	err := scanner.Scan(
		&node.NodeID,
		&node.Hostname,
		&node.Status,
		&node.StartedAt,
		&node.LastHeartbeatAt,
		&markedOfflineAt,
	)
	if err != nil {
		return nil, err
	}
	node.MarkedOfflineAt = markedOfflineAt
	return &node, nil
}
