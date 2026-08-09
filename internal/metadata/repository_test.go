package metadata

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestRepository(t *testing.T) *SQLiteRepository {
	t.Helper()
	repo, err := NewSQLiteRepository("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate repository: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})
	return repo
}

func sampleRecord(id string, checksum string, createdAt time.Time, ttl time.Duration) *FileRecord {
	return &FileRecord{
		ID:             id,
		OriginalName:   "example.png",
		ContentType:    "image/png",
		SizeBytes:      1024,
		ChecksumSHA256: checksum,
		ObjectKey:      "2026/08/09/" + id + ".png",
		CDNURL:         "https://cdn.tempcdn.example.com/2026/08/09/" + id + ".png",
		UploaderIPHash: "hashed-ip",
		CreatedAt:      createdAt,
		ExpiresAt:      createdAt.Add(ttl),
	}
}

func TestInsertAndFindByID(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	record := sampleRecord("id-1", "checksum-1", now, 24*time.Hour)
	if err := repo.Insert(ctx, record); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	found, err := repo.FindByID(ctx, "id-1")
	if err != nil {
		t.Fatalf("find by id failed: %v", err)
	}
	if found.ChecksumSHA256 != "checksum-1" {
		t.Errorf("expected checksum-1, got %s", found.ChecksumSHA256)
	}
}

func TestFindByIDNotFound(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	_, err := repo.FindByID(ctx, "does-not-exist")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound, got %v", err)
	}
}

func TestFindActiveByChecksumReturnsActiveRecord(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	record := sampleRecord("id-active", "shared-checksum", now, 24*time.Hour)
	if err := repo.Insert(ctx, record); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	found, err := repo.FindActiveByChecksum(ctx, "shared-checksum", now)
	if err != nil {
		t.Fatalf("find active by checksum failed: %v", err)
	}
	if found.ID != "id-active" {
		t.Errorf("expected id-active, got %s", found.ID)
	}
}

func TestFindActiveByChecksumIgnoresExpiredRecord(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-48 * time.Hour)

	expiredRecord := sampleRecord("id-expired", "expired-checksum", past, 24*time.Hour)
	if err := repo.Insert(ctx, expiredRecord); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	_, err := repo.FindActiveByChecksum(ctx, "expired-checksum", time.Now().UTC())
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound for expired record, got %v", err)
	}
}

func TestDeleteByID(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	record := sampleRecord("id-to-delete", "checksum-delete", now, 24*time.Hour)
	if err := repo.Insert(ctx, record); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	if err := repo.DeleteByID(ctx, "id-to-delete"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	_, err := repo.FindByID(ctx, "id-to-delete")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound after delete, got %v", err)
	}
}

func TestDeleteByIDNotFound(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	err := repo.DeleteByID(ctx, "never-existed")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound, got %v", err)
	}
}
