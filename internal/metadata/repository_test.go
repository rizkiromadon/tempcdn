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

// TestMigrateIsIdempotent guards against a real regression that shipped
// once already: Migrate() has no applied-migrations tracking table and
// simply re-executes every file in migrations/ on every process startup
// (see repository.go), so every individual step must itself be safe to run
// more than once - plain SQL via CREATE TABLE/INDEX IF NOT EXISTS, and the
// delete_token_hash column via ensureColumn(), which checks PRAGMA
// table_info() before altering rather than relying on "ADD COLUMN IF NOT
// EXISTS" (that SQLite 3.35+ syntax isn't available on every go-sqlite3
// build - it broke exactly this way once). A restart of the server is
// exactly the scenario that would hit this.
func TestMigrateIsIdempotent(t *testing.T) {
	repo, err := NewSQLiteRepository("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("first migrate failed: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("second migrate (simulating a server restart) failed: %v", err)
	}
}

// TestEnsureColumnDoesNotDeadlockOnSingleConnectionPool exercises the fix
// for the specific bug this guarded against: SetMaxOpenConns(1) means only
// one connection exists for the whole process, so ensureColumn's PRAGMA
// table_info query must be fully closed before it issues ALTER TABLE on
// that same connection, or the ALTER TABLE blocks forever waiting for a
// connection that ensureColumn itself is still holding.
func TestEnsureColumnDoesNotDeadlockOnSingleConnectionPool(t *testing.T) {
	repo, err := NewSQLiteRepository("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if _, err := repo.db.ExecContext(ctx, `CREATE TABLE widgets (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("failed to create test table: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- repo.ensureColumn(ctx, "widgets", "extra_field", "TEXT NOT NULL DEFAULT ''")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ensureColumn failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ensureColumn appears to have deadlocked")
	}

	// A second call must be a no-op (column now exists) and must not
	// deadlock either.
	if err := repo.ensureColumn(ctx, "widgets", "extra_field", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("second ensureColumn call failed: %v", err)
	}
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

func TestFindExpiredReturnsOnlyExpiredRecordsOldestFirst(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	active := sampleRecord("id-active", "checksum-active", now, 24*time.Hour)
	if err := repo.Insert(ctx, active); err != nil {
		t.Fatalf("insert active record failed: %v", err)
	}

	expiredOlder := sampleRecord("id-expired-older", "checksum-expired-older", now.Add(-48*time.Hour), 1*time.Hour)
	if err := repo.Insert(ctx, expiredOlder); err != nil {
		t.Fatalf("insert older expired record failed: %v", err)
	}

	expiredNewer := sampleRecord("id-expired-newer", "checksum-expired-newer", now.Add(-2*time.Hour), 1*time.Hour)
	if err := repo.Insert(ctx, expiredNewer); err != nil {
		t.Fatalf("insert newer expired record failed: %v", err)
	}

	found, err := repo.FindExpired(ctx, now, 10)
	if err != nil {
		t.Fatalf("find expired failed: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 expired records, got %d", len(found))
	}
	if found[0].ID != "id-expired-older" || found[1].ID != "id-expired-newer" {
		t.Errorf("expected oldest-expiry-first ordering, got %s then %s", found[0].ID, found[1].ID)
	}
}

func TestFindExpiredRespectsLimit(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)

	for i := 0; i < 5; i++ {
		id := "id-expired-" + string(rune('a'+i))
		record := sampleRecord(id, "checksum-"+id, past, 1*time.Hour)
		if err := repo.Insert(ctx, record); err != nil {
			t.Fatalf("insert failed: %v", err)
		}
	}

	found, err := repo.FindExpired(ctx, now, 3)
	if err != nil {
		t.Fatalf("find expired failed: %v", err)
	}
	if len(found) != 3 {
		t.Errorf("expected limit of 3 records, got %d", len(found))
	}
}
