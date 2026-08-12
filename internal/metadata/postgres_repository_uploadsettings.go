package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file implements UploadSettingsRepository on *PostgresRepository.
// It owns everything about the singleton `upload_settings` row (id = 1).

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
