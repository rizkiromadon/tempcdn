package metadata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file implements AdminRepository on *PostgresRepository.
// It owns everything about the `admins` and `admin_sessions` tables.

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
