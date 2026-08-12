package upload

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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

func TestSyncOnceToleratesRepositoryError(t *testing.T) {
	repo := newMockRepository()
	validator := NewValidator(100*1024*1024, []string{"image/*"}, []string{".exe"})
	sync := NewSettingsSynchronizer(repo, validator, discardLogger())

	sync.syncOnce(context.Background())

	maxSize, _, _ := validator.Snapshot()
	if maxSize != 100*1024*1024 {
		t.Errorf("expected validator to keep its last-known-good limit after a failed poll, got %d bytes", maxSize)
	}
}

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

	case <-time.After(2 * time.Second):
		t.Fatal("expected Run to return shortly after context cancellation, but it kept running")
	}
}
