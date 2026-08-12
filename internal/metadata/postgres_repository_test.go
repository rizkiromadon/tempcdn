package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func sampleRecord(id, checksum string, createdAt time.Time, ttl time.Duration) *FileRecord {
	return &FileRecord{
		ID:              id,
		OriginalName:    "test-file.bin",
		ContentType:     "application/octet-stream",
		SizeBytes:       1024,
		ChecksumSHA256:  checksum,
		ObjectKey:       "test/" + id,
		CDNURL:          "https://cdn.example.com/test/" + id,
		UploaderIPHash:  "test-ip-hash",
		DeleteTokenHash: "test-delete-token-hash-" + id,
		CreatedAt:       createdAt,
		ExpiresAt:       createdAt.Add(ttl),
	}
}

func newTestPostgresRepository(t *testing.T) *PostgresRepository {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping Postgres-backed test")
	}

	ctx := context.Background()
	repo, err := NewPostgresRepository(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("failed to connect to test postgres: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		repo.Close()
		t.Fatalf("failed to migrate test postgres: %v", err)
	}

	if _, err := repo.pool.Exec(ctx, `TRUNCATE TABLE files`); err != nil {
		repo.Close()
		t.Fatalf("failed to truncate files table: %v", err)
	}

	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestPostgresMigrateIsIdempotent(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("second migrate (simulating a server restart) failed: %v", err)
	}
}

func TestPostgresMigrateConcurrentStartupIsSafe(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping Postgres-backed test")
	}
	ctx := context.Background()

	const instanceCount = 5
	repos := make([]*PostgresRepository, instanceCount)
	for i := range repos {

		repo, err := NewPostgresRepository(ctx, dsn, 1)
		if err != nil {
			t.Fatalf("failed to connect instance %d: %v", i, err)
		}
		t.Cleanup(func() { repo.Close() })
		repos[i] = repo
	}

	var wg sync.WaitGroup
	errs := make([]error, instanceCount)
	for i, repo := range repos {
		wg.Add(1)
		go func(i int, repo *PostgresRepository) {
			defer wg.Done()
			errs[i] = repo.Migrate(ctx)
		}(i, repo)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d Migrate() failed: %v", i, err)
		}
	}
}

func TestPostgresFindExpiredSkipsRowsLockedByAnotherSweeper(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)

	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("id-expired-%d", i)
		record := sampleRecord(id, "checksum-"+id, past, 1*time.Minute)
		if err := repo.Insert(ctx, record); err != nil {
			t.Fatalf("insert failed: %v", err)
		}
	}

	const perCallLimit = 5
	var wg sync.WaitGroup
	results := make([][]*FileRecord, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.FindExpired(ctx, now, perCallLimit)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("FindExpired call %d failed: %v", i, err)
		}
	}

	seen := make(map[string]int)
	for _, result := range results {
		for _, record := range result {
			seen[record.ID]++
		}
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("record %s was returned by more than one concurrent FindExpired call (count=%d) - SKIP LOCKED did not prevent overlap", id, count)
		}
	}
}

func TestPostgresSeedUploadSettingsIfMissingIsIdempotent(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if _, err := repo.pool.Exec(ctx, `DELETE FROM upload_settings`); err != nil {
		t.Fatalf("failed to clear upload_settings table: %v", err)
	}

	first := &UploadSettings{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
		UpdatedAt:         time.Now().UTC(),
	}
	if err := repo.SeedUploadSettingsIfMissing(ctx, first); err != nil {
		t.Fatalf("first seed failed: %v", err)
	}

	changedNow := time.Now().UTC()
	changed := &UploadSettings{
		MaxUploadSizeMB:   250,
		AllowedMimeTypes:  []string{"image/*", "application/pdf"},
		BlockedExtensions: []string{".exe", ".bat"},
	}
	if err := repo.UpdateUploadSettings(ctx, changed, "test-admin-id", changedNow); err != nil {
		t.Fatalf("update after seed failed: %v", err)
	}

	if err := repo.SeedUploadSettingsIfMissing(ctx, first); err != nil {
		t.Fatalf("second seed (simulating a restart) failed: %v", err)
	}

	got, err := repo.GetUploadSettings(ctx)
	if err != nil {
		t.Fatalf("get upload settings failed: %v", err)
	}
	if got.MaxUploadSizeMB != 250 {
		t.Errorf("expected admin-changed max_upload_size_mb=250 to survive re-seed, got %d", got.MaxUploadSizeMB)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != "test-admin-id" {
		t.Errorf("expected updated_by to still reflect the admin change, got %v", got.UpdatedBy)
	}
}

func TestPostgresUpdateUploadSettingsFailsWhenRowMissing(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if _, err := repo.pool.Exec(ctx, `DELETE FROM upload_settings`); err != nil {
		t.Fatalf("failed to clear upload_settings table: %v", err)
	}

	err := repo.UpdateUploadSettings(ctx, &UploadSettings{
		MaxUploadSizeMB:   50,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: nil,
	}, "test-admin-id", time.Now().UTC())
	if !errors.Is(err, ErrUploadSettingsNotFound) {
		t.Fatalf("expected ErrUploadSettingsNotFound, got %v", err)
	}
}

func TestPostgresSeedLegalDocumentIfMissingIsIdempotent(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if _, err := repo.pool.Exec(ctx, `DELETE FROM legal_documents WHERE doc_type = $1`, LegalDocTerms); err != nil {
		t.Fatalf("failed to clear legal_documents row: %v", err)
	}

	first := &LegalDocument{
		DocType:   LegalDocTerms,
		Content:   "Default terms.",
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.SeedLegalDocumentIfMissing(ctx, first); err != nil {
		t.Fatalf("first seed failed: %v", err)
	}

	changedNow := time.Now().UTC()
	if _, err := repo.UpdateLegalDocument(ctx, LegalDocTerms, "Admin-edited terms.", "test-admin-id", changedNow); err != nil {
		t.Fatalf("update after seed failed: %v", err)
	}

	if err := repo.SeedLegalDocumentIfMissing(ctx, first); err != nil {
		t.Fatalf("second seed (simulating a restart) failed: %v", err)
	}

	got, err := repo.GetLegalDocument(ctx, LegalDocTerms)
	if err != nil {
		t.Fatalf("get legal document failed: %v", err)
	}
	if got.Content != "Admin-edited terms." {
		t.Errorf("expected admin-changed content to survive re-seed, got %q", got.Content)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != "test-admin-id" {
		t.Errorf("expected updated_by to still reflect the admin change, got %v", got.UpdatedBy)
	}
}

func TestPostgresUpdateLegalDocumentFailsWhenRowMissing(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if _, err := repo.pool.Exec(ctx, `DELETE FROM legal_documents WHERE doc_type = $1`, LegalDocPrivacy); err != nil {
		t.Fatalf("failed to clear legal_documents row: %v", err)
	}

	_, err := repo.UpdateLegalDocument(ctx, LegalDocPrivacy, "New content.", "test-admin-id", time.Now().UTC())
	if !errors.Is(err, ErrLegalDocumentNotFound) {
		t.Fatalf("expected ErrLegalDocumentNotFound, got %v", err)
	}
}

func TestPostgresGetLegalDocumentFailsWhenRowMissing(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if _, err := repo.pool.Exec(ctx, `DELETE FROM legal_documents WHERE doc_type = $1`, LegalDocPrivacy); err != nil {
		t.Fatalf("failed to clear legal_documents row: %v", err)
	}

	_, err := repo.GetLegalDocument(ctx, LegalDocPrivacy)
	if !errors.Is(err, ErrLegalDocumentNotFound) {
		t.Fatalf("expected ErrLegalDocumentNotFound, got %v", err)
	}
}
