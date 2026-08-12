package metadata

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func (r *PostgresRepository) InsertAdmin(ctx context.Context, admin *Admin) error {
	const query = `
		INSERT INTO admins (id, username, password_hash, created_at, last_login_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := r.pool.Exec(ctx, query,
		admin.ID,
		admin.Username,
		admin.PasswordHash,
		admin.CreatedAt,
		admin.LastLoginAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAdminUsernameTaken
		}
		return fmt.Errorf("insert admin: %w", err)
	}
	return nil
}

func (r *PostgresRepository) FindAdminByUsername(ctx context.Context, username string) (*Admin, error) {
	const query = `
		SELECT id, username, password_hash, created_at, last_login_at
		FROM admins
		WHERE username = $1
	`
	row := r.pool.QueryRow(ctx, query, username)
	admin, err := pgScanAdminRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan admin: %w", err)
	}
	return admin, nil
}

func (r *PostgresRepository) FindAdminByID(ctx context.Context, id string) (*Admin, error) {
	const query = `
		SELECT id, username, password_hash, created_at, last_login_at
		FROM admins
		WHERE id = $1
	`
	row := r.pool.QueryRow(ctx, query, id)
	admin, err := pgScanAdminRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdminNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan admin: %w", err)
	}
	return admin, nil
}

func (r *PostgresRepository) CountAdmins(ctx context.Context) (int64, error) {
	var count int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return count, nil
}

func (r *PostgresRepository) TouchAdminLastLogin(ctx context.Context, adminID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE admins SET last_login_at = $2 WHERE id = $1`, adminID, now)
	if err != nil {
		return fmt.Errorf("touch admin last login: %w", err)
	}
	return nil
}

func (r *PostgresRepository) InsertAdminSession(ctx context.Context, session *AdminSession) error {
	const query = `
		INSERT INTO admin_sessions (token_hash, admin_id, created_at, expires_at, last_used_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := r.pool.Exec(ctx, query,
		session.TokenHash,
		session.AdminID,
		session.CreatedAt,
		session.ExpiresAt,
		session.LastUsedAt,
	)
	if err != nil {
		return fmt.Errorf("insert admin session: %w", err)
	}
	return nil
}

func (r *PostgresRepository) FindAdminSessionByTokenHash(ctx context.Context, tokenHash string) (*AdminSession, error) {
	const query = `
		SELECT token_hash, admin_id, created_at, expires_at, last_used_at
		FROM admin_sessions
		WHERE token_hash = $1
	`
	row := r.pool.QueryRow(ctx, query, tokenHash)
	session, err := pgScanAdminSessionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdminSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan admin session: %w", err)
	}
	return session, nil
}

func (r *PostgresRepository) TouchAdminSession(ctx context.Context, tokenHash string, now time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE admin_sessions SET last_used_at = $2 WHERE token_hash = $1`,
		tokenHash, now,
	)
	if err != nil {
		return fmt.Errorf("touch admin session: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete admin session: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeleteExpiredAdminSessions(ctx context.Context, before time.Time) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at <= $1`, before)
	if err != nil {
		return fmt.Errorf("delete expired admin sessions: %w", err)
	}
	return nil
}

func (r *PostgresRepository) InsertAPIKey(ctx context.Context, key *APIKey) error {
	const query = `
		INSERT INTO api_keys (id, name, token_hash, created_at, last_used_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := r.pool.Exec(ctx, query,
		key.ID,
		key.Name,
		key.TokenHash,
		key.CreatedAt,
		key.LastUsedAt,
		key.RevokedAt,
	)
	if err != nil {
		return fmt.Errorf("insert api key: %w", err)
	}
	return nil
}

func (r *PostgresRepository) FindAPIKeyByTokenHash(ctx context.Context, tokenHash string) (*APIKey, error) {
	const query = `
		SELECT id, name, token_hash, created_at, last_used_at, revoked_at
		FROM api_keys
		WHERE token_hash = $1
	`
	row := r.pool.QueryRow(ctx, query, tokenHash)
	key, err := pgScanAPIKeyRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan api key: %w", err)
	}
	return key, nil
}

func (r *PostgresRepository) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	const query = `
		SELECT id, name, token_hash, created_at, last_used_at, revoked_at
		FROM api_keys
		ORDER BY created_at DESC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		key, err := pgScanAPIKeyRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api key row: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api key rows: %w", err)
	}
	return keys, nil
}

func (r *PostgresRepository) TouchAPIKey(ctx context.Context, id string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, id, now)
	if err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}

func (r *PostgresRepository) RevokeAPIKey(ctx context.Context, id string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE api_keys SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, now)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetUploadSettings(ctx context.Context) (*UploadSettings, error) {
	const query = `
		SELECT max_upload_size_mb, allowed_mime_types, blocked_extensions, updated_at, updated_by
		FROM upload_settings
		WHERE id = 1
	`
	row := r.pool.QueryRow(ctx, query)
	settings, err := pgScanUploadSettingsRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadSettingsNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan upload settings: %w", err)
	}
	return settings, nil
}

func (r *PostgresRepository) SeedUploadSettingsIfMissing(ctx context.Context, settings *UploadSettings) error {
	allowedJSON, err := json.Marshal(settings.AllowedMimeTypes)
	if err != nil {
		return fmt.Errorf("marshal allowed mime types: %w", err)
	}
	blockedJSON, err := json.Marshal(settings.BlockedExtensions)
	if err != nil {
		return fmt.Errorf("marshal blocked extensions: %w", err)
	}

	const query = `
		INSERT INTO upload_settings (id, max_upload_size_mb, allowed_mime_types, blocked_extensions, updated_at, updated_by)
		VALUES (1, $1, $2, $3, $4, NULL)
		ON CONFLICT (id) DO NOTHING
	`
	if _, err := r.pool.Exec(ctx, query, settings.MaxUploadSizeMB, allowedJSON, blockedJSON, settings.UpdatedAt); err != nil {
		return fmt.Errorf("seed upload settings: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateUploadSettings(ctx context.Context, settings *UploadSettings, updatedBy string, now time.Time) error {
	allowedJSON, err := json.Marshal(settings.AllowedMimeTypes)
	if err != nil {
		return fmt.Errorf("marshal allowed mime types: %w", err)
	}
	blockedJSON, err := json.Marshal(settings.BlockedExtensions)
	if err != nil {
		return fmt.Errorf("marshal blocked extensions: %w", err)
	}

	const query = `
		UPDATE upload_settings
		SET max_upload_size_mb = $1, allowed_mime_types = $2, blocked_extensions = $3, updated_at = $4, updated_by = $5
		WHERE id = 1
	`
	tag, err := r.pool.Exec(ctx, query, settings.MaxUploadSizeMB, allowedJSON, blockedJSON, now, updatedBy)
	if err != nil {
		return fmt.Errorf("update upload settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUploadSettingsNotFound
	}
	return nil
}

func (r *PostgresRepository) Close() error {
	r.pool.Close()
	return nil
}

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

func pgScanAdminRow(scanner pgRowScanner) (*Admin, error) {
	var admin Admin
	var lastLoginAt *time.Time
	err := scanner.Scan(
		&admin.ID,
		&admin.Username,
		&admin.PasswordHash,
		&admin.CreatedAt,
		&lastLoginAt,
	)
	if err != nil {
		return nil, err
	}
	admin.LastLoginAt = lastLoginAt
	return &admin, nil
}

func pgScanAdminSessionRow(scanner pgRowScanner) (*AdminSession, error) {
	var session AdminSession
	err := scanner.Scan(
		&session.TokenHash,
		&session.AdminID,
		&session.CreatedAt,
		&session.ExpiresAt,
		&session.LastUsedAt,
	)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func pgScanAPIKeyRow(scanner pgRowScanner) (*APIKey, error) {
	var key APIKey
	var lastUsedAt *time.Time
	var revokedAt *time.Time
	err := scanner.Scan(
		&key.ID,
		&key.Name,
		&key.TokenHash,
		&key.CreatedAt,
		&lastUsedAt,
		&revokedAt,
	)
	if err != nil {
		return nil, err
	}
	key.LastUsedAt = lastUsedAt
	key.RevokedAt = revokedAt
	return &key, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func pgScanUploadSettingsRow(scanner pgRowScanner) (*UploadSettings, error) {
	var settings UploadSettings
	var allowedJSON []byte
	var blockedJSON []byte
	var updatedBy *string
	err := scanner.Scan(
		&settings.MaxUploadSizeMB,
		&allowedJSON,
		&blockedJSON,
		&settings.UpdatedAt,
		&updatedBy,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(allowedJSON, &settings.AllowedMimeTypes); err != nil {
		return nil, fmt.Errorf("unmarshal allowed mime types: %w", err)
	}
	if err := json.Unmarshal(blockedJSON, &settings.BlockedExtensions); err != nil {
		return nil, fmt.Errorf("unmarshal blocked extensions: %w", err)
	}
	settings.UpdatedBy = updatedBy
	return &settings, nil
}
