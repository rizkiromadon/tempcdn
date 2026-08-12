package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/idgen"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"golang.org/x/crypto/bcrypt"
)

func newTestAdmin(t *testing.T, repo *fakeRepository, username, password string) *metadata.Admin {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}
	admin := &metadata.Admin{
		ID:           idgen.NewAdminID(),
		Username:     username,
		PasswordHash: string(hash),
		CreatedAt:    time.Now().UTC(),
	}
	if err := repo.InsertAdmin(context.Background(), admin); err != nil {
		t.Fatalf("failed to insert test admin: %v", err)
	}
	return admin
}

func TestLoginSucceedsWithCorrectCredentialsAndCreatesSession(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	result, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("expected login to succeed, got error: %v", err)
	}
	if result.Token == "" {
		t.Error("expected a non-empty session token")
	}
	if result.Admin.Username != "alice" {
		t.Errorf("expected admin username 'alice', got %q", result.Admin.Username)
	}

	if _, exists := repo.sessionsByHash[result.Token]; exists {
		t.Error("plaintext token must not be usable as a stored hash key")
	}
	if _, exists := repo.sessionsByHash[idgen.HashSessionToken(result.Token)]; !exists {
		t.Error("expected a session row keyed by the token's hash")
	}
}

func TestLoginFailsWithWrongPassword(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	_, err := svc.Login(context.Background(), "alice", "wrong-password")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginFailsWithUnknownUsername(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)

	_, err := svc.Login(context.Background(), "nobody", "whatever-password")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginFailsWithEmptyUsernameOrPassword(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	if _, err := svc.Login(context.Background(), "", "correct-horse-battery-staple"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials for empty username, got %v", err)
	}
	if _, err := svc.Login(context.Background(), "alice", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials for empty password, got %v", err)
	}
}

func TestVerifySessionSucceedsWithValidToken(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	session, err := svc.VerifySession(context.Background(), loginResult.Token)
	if err != nil {
		t.Fatalf("expected VerifySession to succeed, got: %v", err)
	}
	if session.Admin.Username != "alice" {
		t.Errorf("expected resolved admin 'alice', got %q", session.Admin.Username)
	}
}

func TestVerifySessionFailsWithEmptyToken(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)

	_, err := svc.VerifySession(context.Background(), "")
	if !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("expected ErrSessionInvalid for empty token, got %v", err)
	}
}

func TestVerifySessionFailsWithUnknownToken(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)

	_, err := svc.VerifySession(context.Background(), "some-token-that-was-never-issued")
	if !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("expected ErrSessionInvalid for unknown token, got %v", err)
	}
}

func TestVerifySessionFailsAndCleansUpExpiredSession(t *testing.T) {
	repo := newFakeRepository()
	acct := newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	token := "test-token-1234"
	tokenHash := idgen.HashSessionToken(token)
	past := time.Now().UTC().Add(-1 * time.Hour)
	if err := repo.InsertAdminSession(context.Background(), &metadata.AdminSession{
		TokenHash:  tokenHash,
		AdminID:    acct.ID,
		CreatedAt:  past.Add(-1 * time.Hour),
		ExpiresAt:  past,
		LastUsedAt: past.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("failed to seed expired session: %v", err)
	}

	_, err := svc.VerifySession(context.Background(), token)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("expected ErrSessionInvalid for expired token, got %v", err)
	}
	if _, exists := repo.sessionsByHash[tokenHash]; exists {
		t.Error("expected expired session to be deleted as a side effect of VerifySession")
	}
}

func TestLogoutRevokesSessionSoItCanNoLongerBeVerified(t *testing.T) {
	repo := newFakeRepository()
	newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")
	svc := NewService(repo)

	loginResult, err := svc.Login(context.Background(), "alice", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if err := svc.Logout(context.Background(), loginResult.Token); err != nil {
		t.Fatalf("expected logout to succeed, got: %v", err)
	}

	if _, err := svc.VerifySession(context.Background(), loginResult.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("expected session to be invalid after logout, got %v", err)
	}
}

func TestLogoutIsIdempotent(t *testing.T) {
	repo := newFakeRepository()
	svc := NewService(repo)

	if err := svc.Logout(context.Background(), "never-issued-token"); err != nil {
		t.Errorf("expected logout of unknown token to succeed, got: %v", err)
	}
	if err := svc.Logout(context.Background(), ""); err != nil {
		t.Errorf("expected logout of empty token to succeed, got: %v", err)
	}
}

func seedTestUploadSettings(t *testing.T, repo *fakeRepository) {
	t.Helper()
	if err := repo.SeedUploadSettingsIfMissing(context.Background(), &metadata.UploadSettings{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{".exe"},
		UpdatedAt:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("failed to seed upload settings: %v", err)
	}
}

func TestGetUploadSettingsReturnsSeededValues(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)

	settings, err := svc.GetUploadSettings(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if settings.MaxUploadSizeMB != 100 {
		t.Errorf("expected max_upload_size_mb=100, got %d", settings.MaxUploadSizeMB)
	}
}

func TestUpdateUploadSettingsPersistsAndStampsUpdatedBy(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)
	admin := newTestAdmin(t, repo, "alice", "correct-horse-battery-staple")

	updated, err := svc.UpdateUploadSettings(context.Background(), admin.ID, UpdateUploadSettingsInput{
		MaxUploadSizeMB:   250,
		AllowedMimeTypes:  []string{"image/*", "application/pdf"},
		BlockedExtensions: []string{".exe", ".bat"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if updated.MaxUploadSizeMB != 250 {
		t.Errorf("expected max_upload_size_mb=250, got %d", updated.MaxUploadSizeMB)
	}
	if updated.UpdatedBy == nil || *updated.UpdatedBy != admin.ID {
		t.Errorf("expected updated_by=%s, got %v", admin.ID, updated.UpdatedBy)
	}

	got, err := svc.GetUploadSettings(context.Background())
	if err != nil {
		t.Fatalf("expected no error re-reading settings, got %v", err)
	}
	if got.MaxUploadSizeMB != 250 {
		t.Errorf("expected persisted max_upload_size_mb=250, got %d", got.MaxUploadSizeMB)
	}
}

func TestUpdateUploadSettingsRejectsNonPositiveSize(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)

	_, err := svc.UpdateUploadSettings(context.Background(), "admin-id", UpdateUploadSettingsInput{
		MaxUploadSizeMB:   0,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: nil,
	})
	if !errors.Is(err, ErrInvalidUploadSettings) {
		t.Errorf("expected ErrInvalidUploadSettings for zero max size, got %v", err)
	}
}

func TestUpdateUploadSettingsRejectsSizeAboveCeiling(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)

	_, err := svc.UpdateUploadSettings(context.Background(), "admin-id", UpdateUploadSettingsInput{
		MaxUploadSizeMB:   maxUploadSizeMBCeiling + 1,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: nil,
	})
	if !errors.Is(err, ErrInvalidUploadSettings) {
		t.Errorf("expected ErrInvalidUploadSettings for size above ceiling, got %v", err)
	}
}

func TestUpdateUploadSettingsRejectsEmptyMimeAllowlist(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)

	_, err := svc.UpdateUploadSettings(context.Background(), "admin-id", UpdateUploadSettingsInput{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"   "},
		BlockedExtensions: nil,
	})
	if !errors.Is(err, ErrInvalidUploadSettings) {
		t.Errorf("expected ErrInvalidUploadSettings for empty (whitespace-only) mime allowlist, got %v", err)
	}
}

func TestUpdateUploadSettingsRejectsExtensionWithoutDot(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)

	_, err := svc.UpdateUploadSettings(context.Background(), "admin-id", UpdateUploadSettingsInput{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{"image/*"},
		BlockedExtensions: []string{"exe"},
	})
	if !errors.Is(err, ErrInvalidUploadSettings) {
		t.Errorf("expected ErrInvalidUploadSettings for extension missing a leading dot, got %v", err)
	}
}

func TestUpdateUploadSettingsTrimsAndDropsEmptyEntries(t *testing.T) {
	repo := newFakeRepository()
	seedTestUploadSettings(t, repo)
	svc := NewService(repo)

	updated, err := svc.UpdateUploadSettings(context.Background(), "admin-id", UpdateUploadSettingsInput{
		MaxUploadSizeMB:   100,
		AllowedMimeTypes:  []string{" image/* ", "", "application/pdf"},
		BlockedExtensions: []string{" .exe ", ""},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(updated.AllowedMimeTypes) != 2 || updated.AllowedMimeTypes[0] != "image/*" || updated.AllowedMimeTypes[1] != "application/pdf" {
		t.Errorf("expected trimmed, non-empty mime types [image/* application/pdf], got %v", updated.AllowedMimeTypes)
	}
	if len(updated.BlockedExtensions) != 1 || updated.BlockedExtensions[0] != ".exe" {
		t.Errorf("expected trimmed, non-empty blocked extensions [.exe], got %v", updated.BlockedExtensions)
	}
}
