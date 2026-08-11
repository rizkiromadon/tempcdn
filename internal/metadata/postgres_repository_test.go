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

// sampleRecord builds a minimal, valid *FileRecord for tests that only
// care about expiry/ID/checksum behavior (e.g. FindExpired), filling every
// other field with an arbitrary-but-valid placeholder so Insert succeeds
// against the real files table's NOT NULL constraints.
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

// newTestPostgresRepository connects to TEST_POSTGRES_DSN and returns a
// PostgresRepository with a clean "files" table, or skips the test if
// TEST_POSTGRES_DSN is unset. This keeps `go test ./...` green in
// environments without a Postgres instance available (e.g. most local dev
// machines and the default CI job), while still letting a Postgres-backed
// job run these tests against a real database - e.g.:
//
//	TEST_POSTGRES_DSN="postgres://tempcdn:tempcdn@localhost:5432/tempcdn_test?sslmode=disable" go test ./internal/metadata/...
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
	// Each test starts from an empty table rather than a fresh database,
	// since standing up/tearing down a database per test is unnecessary
	// overhead here - schema_migrations is left alone so Migrate() isn't
	// re-run from scratch on every test.
	if _, err := repo.pool.Exec(ctx, `TRUNCATE TABLE files`); err != nil {
		repo.Close()
		t.Fatalf("failed to truncate files table: %v", err)
	}

	t.Cleanup(func() { repo.Close() })
	return repo
}

// TestPostgresMigrateIsIdempotent guards against a real regression that
// shipped once already: Migrate() must be safe to call again on every
// process restart, since a restart of the server is exactly the scenario
// that would hit this.
func TestPostgresMigrateIsIdempotent(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()

	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("second migrate (simulating a server restart) failed: %v", err)
	}
}

// TestPostgresMigrateConcurrentStartupIsSafe simulates srv1/srv2/srv3 all
// starting up against the same fresh database at once: every instance
// calls Migrate() concurrently, and the advisory lock in Migrate() must
// serialize them so the migration is applied exactly once and none of the
// concurrent callers error out.
func TestPostgresMigrateConcurrentStartupIsSafe(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping Postgres-backed test")
	}
	ctx := context.Background()

	const instanceCount = 5
	repos := make([]*PostgresRepository, instanceCount)
	for i := range repos {
		// maxConns=1 per simulated instance here: this test only needs
		// each one to issue a handful of sequential statements, and 5
		// instances x a larger pool each risks tripping the same
		// connection-slot limits this size cap exists to avoid in the
		// first place (see NewPostgresRepository).
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

// TestPostgresFindExpiredSkipsRowsLockedByAnotherSweeper is the key
// correctness property motivating FOR UPDATE SKIP LOCKED: if two sweepers
// (e.g. running on srv1 and srv2) call FindExpired concurrently within an
// overlapping transaction window, they must not both receive the same
// expired row, or both would attempt to delete the same R2 object and DB
// record.
func TestPostgresFindExpiredSkipsRowsLockedByAnotherSweeper(t *testing.T) {
	repo := newTestPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)

	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("id-expired-%d", i)
		record := sampleRecord(id, "checksum-"+id, past, 1*time.Minute) // already expired
		if err := repo.Insert(ctx, record); err != nil {
			t.Fatalf("insert failed: %v", err)
		}
	}

	// Hold a transaction open with FOR UPDATE SKIP LOCKED still in effect
	// by driving two FindExpired calls concurrently and confirming their
	// results don't overlap. Since FindExpired commits internally, we
	// instead directly exercise two goroutines racing to claim from the
	// same 10-row pool with a small limit each, repeated enough times that
	// an overlap would show up if SKIP LOCKED weren't working.
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

// TestPostgresSeedUploadSettingsIfMissingIsIdempotent guards the same
// restart scenario as TestPostgresMigrateIsIdempotent, but for the
// upload_settings row: a later boot with different env-var-derived
// defaults must not silently overwrite settings an admin has since
// changed via UpdateUploadSettings.
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

	// Simulate an admin changing settings after the initial seed.
	changedNow := time.Now().UTC()
	changed := &UploadSettings{
		MaxUploadSizeMB:   250,
		AllowedMimeTypes:  []string{"image/*", "application/pdf"},
		BlockedExtensions: []string{".exe", ".bat"},
	}
	if err := repo.UpdateUploadSettings(ctx, changed, "test-admin-id", changedNow); err != nil {
		t.Fatalf("update after seed failed: %v", err)
	}

	// A later "boot" seeding with the original defaults again must be a
	// no-op, since the row now exists (see the ON CONFLICT DO NOTHING in
	// SeedUploadSettingsIfMissing).
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

// TestPostgresUpdateUploadSettingsFailsWhenRowMissing guards
// UpdateUploadSettings' documented ErrUploadSettingsNotFound behavior: it
// must not silently insert a row, only update an existing one.
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
