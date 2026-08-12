package upload

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSyncOncePicksUpChangeMadeByAnotherInstance guards the core scenario
// this file exists for: an admin's PUT /api/v1/admin/upload-settings
// handled by a *different* instance only updates that instance's Validator
// directly (see admin.Handler's onUploadSettingsUpdated callback wiring in
// main.go) - this instance only learns about it by polling the database,
// which is exactly what syncOnce does.
func TestSyncOncePicksUpChangeMadeByAnotherInstance(t *testing.T) {
	repo := newMockRepository()
	repo.uploadSettings = &metadata.UploadSettings{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
		UpdatedAt:         time.Now().UTC(),
	}
	validator := NewValidator(100*1024*1024, []string{"image/*"}, []string{".exe"})
	sync := NewSettingsSynchronizer(repo, validator, discardLogger())

	// Simulate another instance changing settings in the shared database -
	// this instance's Validator doesn't know about it yet.
	repo.uploadSettings = &metadata.UploadSettings{
		MaxUploadSizeMB:   1,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
		UpdatedAt:         time.Now().UTC(),
	}

	maxSizeBefore, _, _ := validator.Snapshot()
	if maxSizeBefore != 100*1024*1024 {
		t.Fatalf("expected validator to still enforce the old 100MB limit before syncing, got %d bytes", maxSizeBefore)
	}

	sync.syncOnce(context.Background())

	maxSizeAfter, _, _ := validator.Snapshot()
	if maxSizeAfter != 1*1024*1024 {
		t.Errorf("expected validator to enforce the new 1MB limit after syncOnce, got %d bytes", maxSizeAfter)
	}
}

// TestSyncOnceIsNoOpWhenNothingChanged guards against needlessly calling
// Validator.Update (and logging) on every poll when the database value
// hasn't actually changed since the last sync.
func TestSyncOnceIsNoOpWhenNothingChanged(t *testing.T) {
	repo := newMockRepository()
	repo.uploadSettings = &metadata.UploadSettings{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*", "application/pdf"},
		BlockedExtensions: []string{".exe", ".bat"},
		UpdatedAt:         time.Now().UTC(),
	}
	validator := NewValidator(
		100*1024*1024,
		[]string{"image/*", "application/pdf"},
		[]string{".exe", ".bat"},
	)
	sync := NewSettingsSynchronizer(repo, validator, discardLogger())

	sync.syncOnce(context.Background())

	maxSize, allowed, blocked := validator.Snapshot()
	if maxSize != 100*1024*1024 {
		t.Errorf("expected max size to remain unchanged at 100MB, got %d bytes", maxSize)
	}
	if len(allowed) != 2 || len(blocked) != 2 {
		t.Errorf("expected allowed/blocked lists to remain unchanged, got %v / %v", allowed, blocked)
	}
}

// TestSyncOnceToleratesRepositoryError guards that a transient database
// error during a poll doesn't panic or corrupt the Validator's current
// (still valid) rules - it should just be logged and retried on the next
// tick.
func TestSyncOnceToleratesRepositoryError(t *testing.T) {
	repo := newMockRepository() // uploadSettings left nil -> GetUploadSettings errors
	validator := NewValidator(100*1024*1024, []string{"image/*"}, []string{".exe"})
	sync := NewSettingsSynchronizer(repo, validator, discardLogger())

	sync.syncOnce(context.Background())

	maxSize, _, _ := validator.Snapshot()
	if maxSize != 100*1024*1024 {
		t.Errorf("expected validator to keep its last-known-good limit after a failed poll, got %d bytes", maxSize)
	}
}

// TestRunStopsWhenContextCancelled guards that Run's goroutine actually
// exits on cancellation instead of leaking forever - important since
// main.go starts this in a bare `go sync.Run(ctx)` with no other shutdown
// signal.
func TestRunStopsWhenContextCancelled(t *testing.T) {
	repo := newMockRepository()
	repo.uploadSettings = &metadata.UploadSettings{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: nil,
		UpdatedAt:         time.Now().UTC(),
	}
	validator := NewValidator(100*1024*1024, []string{"image/*"}, nil)
	sync := NewSettingsSynchronizer(repo, validator, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sync.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run returned promptly after cancellation, as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("expected Run to return shortly after context cancellation, but it kept running")
	}
}
