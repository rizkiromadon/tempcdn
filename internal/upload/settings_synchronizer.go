package upload

import (
	"context"
	"log/slog"
	"reflect"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
)

// settingsSyncInterval is how often each instance re-reads upload_settings
// from the database and applies any change to its in-process Validator.
//
// This is what makes a change made through the admin API (PUT
// /api/v1/admin/upload-settings) on one instance eventually visible on
// every other instance sharing the same database, in a multi-instance
// deployment (see README "Running Multiple Instances") - without this,
// only the instance that handled the PUT request would pick up the new
// limits (via admin.Handler's onUploadSettingsUpdated callback - see
// main.go), and every other instance, plus that same instance after a
// restart wipes its in-memory state, would keep enforcing whatever was
// seeded/loaded at its own last boot.
const settingsSyncInterval = 10 * time.Second

// SettingsSynchronizer periodically re-reads upload_settings from the
// database and pushes any change into a Validator, so every instance
// converges on the same limits within settingsSyncInterval of an admin
// changing them - without needing a restart.
type SettingsSynchronizer struct {
	repository metadata.Repository
	validator  *Validator
	logger     *slog.Logger
}

func NewSettingsSynchronizer(repository metadata.Repository, validator *Validator, logger *slog.Logger) *SettingsSynchronizer {
	return &SettingsSynchronizer{repository: repository, validator: validator, logger: logger}
}

// Run blocks, polling every settingsSyncInterval until ctx is cancelled.
// Call this in its own goroutine, after the Validator has already been
// seeded with the settings current as of process start (see main.go) -
// this only needs to catch changes made *after* that point.
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

// syncOnce performs a single poll-and-apply cycle. Split out from Run so
// it's independently testable without needing a ticker/goroutine.
func (s *SettingsSynchronizer) syncOnce(ctx context.Context) {
	settings, err := s.repository.GetUploadSettings(ctx)
	if err != nil {
		// Transient DB error or (in practice unreachable post-boot, since
		// main.go seeds the row before starting this goroutine) a missing
		// row. Either way, keep enforcing whatever the Validator already
		// has rather than erroring out - a stale-but-valid rule set is
		// always safer than crashing the poller loop.
		s.logger.Error("upload_settings_sync_failed", "error", err)
		return
	}

	currentMaxSize, currentAllowed, currentBlocked := s.validator.Snapshot()
	newMaxSize := settings.MaxUploadSizeMB * 1024 * 1024
	if newMaxSize == currentMaxSize &&
		reflect.DeepEqual(currentAllowed, settings.AllowedMimeTypes) &&
		reflect.DeepEqual(currentBlocked, settings.BlockedExtensions) {
		// Nothing changed since the last poll - skip the update (and the
		// log line below) rather than atomically swapping in an
		// identical value every 10s.
		return
	}

	s.validator.Update(newMaxSize, settings.AllowedMimeTypes, settings.BlockedExtensions)
	s.logger.Info("upload_settings_synced",
		"max_upload_size_mb", settings.MaxUploadSizeMB,
		"allowed_mime_types_count", len(settings.AllowedMimeTypes),
		"blocked_extensions_count", len(settings.BlockedExtensions),
	)
}
