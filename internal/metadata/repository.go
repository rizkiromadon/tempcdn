package metadata

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var ErrFileNotFound = errors.New("file record not found")

type Repository interface {
	Migrate(ctx context.Context) error
	Insert(ctx context.Context, record *FileRecord) error
	FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*FileRecord, error)
	FindByID(ctx context.Context, id string) (*FileRecord, error)
	DeleteByID(ctx context.Context, id string) error
	// FindExpired returns up to limit records whose expires_at is at or
	// before "before", for the background expiry sweep. Ordered oldest
	// expiry first so the most overdue records are cleaned up first.
	FindExpired(ctx context.Context, before time.Time, limit int) ([]*FileRecord, error)
	// Stats aggregates the files table as of "now": only rows that are
	// currently active (expires_at > now) are counted, since expired/deleted
	// rows are physically removed by the sweeper and DELETE /files/{id} (see
	// DeleteByID) rather than flagged - the table never holds a lifetime
	// history. Callers wanting lifetime totals must combine this with a
	// monotonically-increasing counter recorded at upload time (see
	// httpserver.Metrics), not with this table.
	Stats(ctx context.Context, now time.Time) (*Stats, error)
	Close() error
}

// Stats holds aggregate figures over the files currently active in the
// table (see Repository.Stats).
type Stats struct {
	ActiveFileCount int64
	ActiveBytes     int64
	// ContentTypeBreakdown maps top-level MIME type (the part before "/",
	// e.g. "image", "video", "application") to the number of active files
	// of that type. An empty or malformed content_type is grouped under
	// "other".
	ContentTypeBreakdown map[string]int64
}

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(dsn string) (*SQLiteRepository, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration file %s: %w", entry.Name(), err)
		}
		if _, err := r.db.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
	}

	// ALTER TABLE ADD COLUMN cannot be expressed idempotently in portable
	// SQL here: "ADD COLUMN IF NOT EXISTS" requires SQLite 3.35+, which is
	// not guaranteed across every build of github.com/mattn/go-sqlite3, and
	// Migrate() has no applied-migrations tracking table - it re-runs every
	// file in migrations/ on every process startup (see the loop above), so
	// a plain ALTER TABLE ADD COLUMN would fail on the second startup once
	// the column already exists. Checked and applied here in Go instead, so
	// it stays safe to call Migrate() repeatedly regardless of the SQLite
	// version in use. This must run after the migration file loop above
	// (which creates the "files" table) and before the index below (which
	// references the column this adds).
	if err := r.ensureColumn(ctx, "files", "delete_token_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure delete_token_hash column: %w", err)
	}

	if _, err := r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_files_delete_token_hash ON files (delete_token_hash)`); err != nil {
		return fmt.Errorf("create delete_token_hash index: %w", err)
	}

	return nil
}

// ensureColumn adds columnDef to table if a column named columnName doesn't
// already exist, making ALTER TABLE ADD COLUMN safe to call on every
// startup regardless of SQLite version.
func (r *SQLiteRepository) ensureColumn(ctx context.Context, table string, columnName string, columnDef string) error {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("read table info for %s: %w", table, err)
	}

	exists := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan table_info row for %s: %w", table, err)
		}
		if name == columnName {
			exists = true
			break
		}
	}
	rowsErr := rows.Err()
	// Close explicitly, before issuing the ALTER TABLE below, rather than
	// relying on a deferred Close at function return: the connection pool
	// is capped at 1 (see NewSQLiteRepository), so an ALTER TABLE issued
	// while these rows are still open would block waiting for a connection
	// that this same call is holding.
	if closeErr := rows.Close(); closeErr != nil {
		return fmt.Errorf("close table_info rows for %s: %w", table, closeErr)
	}
	if rowsErr != nil {
		return fmt.Errorf("iterate table_info for %s: %w", table, rowsErr)
	}
	if exists {
		return nil
	}

	alterSQL := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, columnName, columnDef)
	if _, err := r.db.ExecContext(ctx, alterSQL); err != nil {
		return fmt.Errorf("add column %s to %s: %w", columnName, table, err)
	}
	return nil
}

func (r *SQLiteRepository) Insert(ctx context.Context, record *FileRecord) error {
	query := `
		INSERT INTO files (
			id, original_name, content_type, size_bytes, checksum_sha256,
			object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
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

func (r *SQLiteRepository) FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*FileRecord, error) {
	query := `
		SELECT id, original_name, content_type, size_bytes, checksum_sha256,
		       object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		FROM files
		WHERE checksum_sha256 = ? AND expires_at > ?
		ORDER BY created_at DESC
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, checksum, now)
	return scanFileRecord(row)
}

func (r *SQLiteRepository) FindByID(ctx context.Context, id string) (*FileRecord, error) {
	query := `
		SELECT id, original_name, content_type, size_bytes, checksum_sha256,
		       object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		FROM files
		WHERE id = ?
	`
	row := r.db.QueryRowContext(ctx, query, id)
	return scanFileRecord(row)
}

func (r *SQLiteRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*FileRecord, error) {
	query := `
		SELECT id, original_name, content_type, size_bytes, checksum_sha256,
		       object_key, cdn_url, uploader_ip_hash, delete_token_hash, created_at, expires_at
		FROM files
		WHERE expires_at <= ?
		ORDER BY expires_at ASC
		LIMIT ?
	`
	rows, err := r.db.QueryContext(ctx, query, before, limit)
	if err != nil {
		return nil, fmt.Errorf("query expired file records: %w", err)
	}
	defer rows.Close()

	var records []*FileRecord
	for rows.Next() {
		record, err := scanFileRecordRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired file records: %w", err)
	}
	return records, nil
}

// Stats aggregates currently-active rows (expires_at > now) in a single pass
// plus a per-row content_type scan, rather than N+1 queries, since the table
// is expected to stay small (active temporary files only) and this keeps the
// endpoint to two queries total regardless of row count.
func (r *SQLiteRepository) Stats(ctx context.Context, now time.Time) (*Stats, error) {
	stats := &Stats{ContentTypeBreakdown: make(map[string]int64)}

	summaryRow := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)
		FROM files
		WHERE expires_at > ?
	`, now)
	if err := summaryRow.Scan(&stats.ActiveFileCount, &stats.ActiveBytes); err != nil {
		return nil, fmt.Errorf("scan active file summary: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT content_type, COUNT(*)
		FROM files
		WHERE expires_at > ?
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

// topLevelMimeType returns the part of a MIME type before "/" (e.g.
// "image/png" -> "image"), grouping multiple subtypes of the same media
// category together in the breakdown. Empty or malformed values (no "/")
// fall back to "other" rather than being silently dropped or panicking on a
// missing separator.
func topLevelMimeType(contentType string) string {
	slashIndex := strings.IndexByte(contentType, '/')
	if slashIndex <= 0 {
		return "other"
	}
	return contentType[:slashIndex]
}

func (r *SQLiteRepository) DeleteByID(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete file record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete result: %w", err)
	}
	if affected == 0 {
		return ErrFileNotFound
	}
	return nil
}

func (r *SQLiteRepository) Close() error {
	return r.db.Close()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanFileRecord(row *sql.Row) (*FileRecord, error) {
	record, err := scanFileRecordRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan file record: %w", err)
	}
	return record, nil
}

func scanFileRecordRow(scanner rowScanner) (*FileRecord, error) {
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

func scanFileRecordRows(rows *sql.Rows) (*FileRecord, error) {
	record, err := scanFileRecordRow(rows)
	if err != nil {
		return nil, fmt.Errorf("scan file record row: %w", err)
	}
	return record, nil
}
