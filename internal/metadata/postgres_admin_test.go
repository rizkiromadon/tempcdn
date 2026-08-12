package metadata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/idgen"
)

func newTestPostgresRepositoryWithAdmins(t *testing.T) *PostgresRepository {
	t.Helper()
	repo := newTestPostgresRepository(t)
	ctx := context.Background()
	if _, err := repo.pool.Exec(ctx, `TRUNCATE TABLE admins CASCADE`); err != nil {
		t.Fatalf("failed to truncate admins table: %v", err)
	}
	return repo
}

func sampleAdmin(username string) *Admin {
	return &Admin{
		ID:           idgen.NewAdminID(),
		Username:     username,
		PasswordHash: "$2a$12$CwTycUXWue0Thq9StjUM0uJ8p1M4gk1EYr9y7Wz4qOfNaqCzWn9E.",
		CreatedAt:    time.Now().UTC(),
	}
}

func TestPostgresInsertAndFindAdminByUsername(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	admin := sampleAdmin("alice")
	if err := repo.InsertAdmin(ctx, admin); err != nil {
		t.Fatalf("insert admin failed: %v", err)
	}

	found, err := repo.FindAdminByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("find admin by username failed: %v", err)
	}
	if found.ID != admin.ID {
		t.Errorf("expected admin ID %q, got %q", admin.ID, found.ID)
	}
	if found.LastLoginAt != nil {
		t.Error("expected LastLoginAt to be nil for a freshly-created admin")
	}
}

func TestPostgresFindAdminByUsernameReturnsNotFoundForUnknownUsername(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	_, err := repo.FindAdminByUsername(ctx, "nobody")
	if !errors.Is(err, ErrAdminNotFound) {
		t.Errorf("expected ErrAdminNotFound, got %v", err)
	}
}

func TestPostgresInsertAdminRejectsDuplicateUsername(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	first := sampleAdmin("alice")
	if err := repo.InsertAdmin(ctx, first); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	second := sampleAdmin("alice")
	err := repo.InsertAdmin(ctx, second)
	if !errors.Is(err, ErrAdminUsernameTaken) {
		t.Errorf("expected ErrAdminUsernameTaken for duplicate username, got %v", err)
	}
}

func TestPostgresCountAdminsReflectsInsertedRows(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	count, err := repo.CountAdmins(ctx)
	if err != nil {
		t.Fatalf("count admins failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 admins on a clean table, got %d", count)
	}

	if err := repo.InsertAdmin(ctx, sampleAdmin("alice")); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if err := repo.InsertAdmin(ctx, sampleAdmin("bob")); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	count, err = repo.CountAdmins(ctx)
	if err != nil {
		t.Fatalf("count admins failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 admins, got %d", count)
	}
}

func TestPostgresTouchAdminLastLoginUpdatesTimestamp(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	admin := sampleAdmin("alice")
	if err := repo.InsertAdmin(ctx, admin); err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	loginTime := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.TouchAdminLastLogin(ctx, admin.ID, loginTime); err != nil {
		t.Fatalf("touch admin last login failed: %v", err)
	}

	found, err := repo.FindAdminByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("find admin by id failed: %v", err)
	}
	if found.LastLoginAt == nil {
		t.Fatal("expected LastLoginAt to be set after TouchAdminLastLogin")
	}
	if !found.LastLoginAt.Equal(loginTime) {
		t.Errorf("expected LastLoginAt %v, got %v", loginTime, *found.LastLoginAt)
	}
}

func TestPostgresAdminSessionLifecycle(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	admin := sampleAdmin("alice")
	if err := repo.InsertAdmin(ctx, admin); err != nil {
		t.Fatalf("insert admin failed: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	session := &AdminSession{
		TokenHash:  idgen.HashSessionToken("some-plaintext-token"),
		AdminID:    admin.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(24 * time.Hour),
		LastUsedAt: now,
	}
	if err := repo.InsertAdminSession(ctx, session); err != nil {
		t.Fatalf("insert admin session failed: %v", err)
	}

	found, err := repo.FindAdminSessionByTokenHash(ctx, session.TokenHash)
	if err != nil {
		t.Fatalf("find admin session failed: %v", err)
	}
	if found.AdminID != admin.ID {
		t.Errorf("expected admin ID %q, got %q", admin.ID, found.AdminID)
	}

	touchTime := now.Add(1 * time.Minute)
	if err := repo.TouchAdminSession(ctx, session.TokenHash, touchTime); err != nil {
		t.Fatalf("touch admin session failed: %v", err)
	}
	found, err = repo.FindAdminSessionByTokenHash(ctx, session.TokenHash)
	if err != nil {
		t.Fatalf("find admin session after touch failed: %v", err)
	}
	if !found.LastUsedAt.Equal(touchTime) {
		t.Errorf("expected LastUsedAt %v after touch, got %v", touchTime, found.LastUsedAt)
	}

	if err := repo.DeleteAdminSession(ctx, session.TokenHash); err != nil {
		t.Fatalf("delete admin session failed: %v", err)
	}
	if _, err := repo.FindAdminSessionByTokenHash(ctx, session.TokenHash); !errors.Is(err, ErrAdminSessionNotFound) {
		t.Errorf("expected ErrAdminSessionNotFound after delete, got %v", err)
	}
}

func TestPostgresDeleteAdminSessionIsIdempotent(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	if err := repo.DeleteAdminSession(ctx, "never-existed-hash"); err != nil {
		t.Errorf("expected deleting a nonexistent session to succeed, got: %v", err)
	}
}

func TestPostgresDeleteExpiredAdminSessionsOnlyRemovesExpired(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	admin := sampleAdmin("alice")
	if err := repo.InsertAdmin(ctx, admin); err != nil {
		t.Fatalf("insert admin failed: %v", err)
	}

	now := time.Now().UTC()
	expiredSession := &AdminSession{
		TokenHash:  idgen.HashSessionToken("expired-token"),
		AdminID:    admin.ID,
		CreatedAt:  now.Add(-48 * time.Hour),
		ExpiresAt:  now.Add(-24 * time.Hour),
		LastUsedAt: now.Add(-48 * time.Hour),
	}
	validSession := &AdminSession{
		TokenHash:  idgen.HashSessionToken("valid-token"),
		AdminID:    admin.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(24 * time.Hour),
		LastUsedAt: now,
	}
	if err := repo.InsertAdminSession(ctx, expiredSession); err != nil {
		t.Fatalf("insert expired session failed: %v", err)
	}
	if err := repo.InsertAdminSession(ctx, validSession); err != nil {
		t.Fatalf("insert valid session failed: %v", err)
	}

	if err := repo.DeleteExpiredAdminSessions(ctx, now); err != nil {
		t.Fatalf("delete expired admin sessions failed: %v", err)
	}

	if _, err := repo.FindAdminSessionByTokenHash(ctx, expiredSession.TokenHash); !errors.Is(err, ErrAdminSessionNotFound) {
		t.Errorf("expected expired session to be deleted, got err=%v", err)
	}
	if _, err := repo.FindAdminSessionByTokenHash(ctx, validSession.TokenHash); err != nil {
		t.Errorf("expected valid session to survive cleanup, got err=%v", err)
	}
}

func TestPostgresAdminSessionCascadeDeletesWhenAdminDeleted(t *testing.T) {
	repo := newTestPostgresRepositoryWithAdmins(t)
	ctx := context.Background()

	admin := sampleAdmin("alice")
	if err := repo.InsertAdmin(ctx, admin); err != nil {
		t.Fatalf("insert admin failed: %v", err)
	}

	now := time.Now().UTC()
	session := &AdminSession{
		TokenHash:  idgen.HashSessionToken("some-token"),
		AdminID:    admin.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(24 * time.Hour),
		LastUsedAt: now,
	}
	if err := repo.InsertAdminSession(ctx, session); err != nil {
		t.Fatalf("insert session failed: %v", err)
	}

	if _, err := repo.pool.Exec(ctx, `DELETE FROM admins WHERE id = $1`, admin.ID); err != nil {
		t.Fatalf("failed to delete admin: %v", err)
	}

	if _, err := repo.FindAdminSessionByTokenHash(ctx, session.TokenHash); !errors.Is(err, ErrAdminSessionNotFound) {
		t.Errorf("expected session to be cascade-deleted along with its admin, got err=%v", err)
	}
}
