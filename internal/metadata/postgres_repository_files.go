package metadata

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file implements FileRepository on *PostgresRepository.
// It owns everything about the `files` table: insert, lookup, expiry, stats.

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

func (r *PostgresRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*FileRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin find-expired transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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

func pgScanFileRecord(row pgx.Row) (*FileRecord, error) {
	record, err := pgScanFileRecordRow(row)
	if err == pgx.ErrNoRows {
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
