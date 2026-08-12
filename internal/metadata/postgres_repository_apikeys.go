package metadata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file implements APIKeyRepository on *PostgresRepository.
// It owns everything about the `api_keys` table.

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
