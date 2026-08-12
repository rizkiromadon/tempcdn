package upload

import (
	"context"
	"log/slog"
	"reflect"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

const settingsSyncInterval = 10 * time.Second

type SettingsSynchronizer struct {
	repository metadata.UploadSettingsRepository
	validator  *Validator
	logger     *slog.Logger
}

func NewSettingsSynchronizer(repository metadata.UploadSettingsRepository, validator *Validator, logger *slog.Logger) *SettingsSynchronizer {
	return &SettingsSynchronizer{repository: repository, validator: validator, logger: logger}
}

func (s *SettingsSynchronizer) Run(ctx context.Context) {
	ticker := time.NewTicker(settingsSyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncOnce(ctx)
		}
	}
}

func (s *SettingsSynchronizer) syncOnce(ctx context.Context) {
	settings, err := s.repository.GetUploadSettings(ctx)
	if err != nil {

		s.logger.Error("upload_settings_sync_failed", "error", err)
		return
	}

	currentMaxSize, currentAllowed, currentBlocked := s.validator.Snapshot()
	newMaxSize := settings.MaxUploadSizeMB * 1024 * 1024
	if newMaxSize == currentMaxSize &&
		reflect.DeepEqual(currentAllowed, settings.AllowedMimeTypes) &&
		reflect.DeepEqual(currentBlocked, settings.BlockedExtensions) {

		return
	}

	s.validator.Update(newMaxSize, settings.AllowedMimeTypes, settings.BlockedExtensions)
	s.logger.Info("upload_settings_synced",
		"max_upload_size_mb", settings.MaxUploadSizeMB,
		"allowed_mime_types_count", len(settings.AllowedMimeTypes),
		"blocked_extensions_count", len(settings.BlockedExtensions),
	)
}
