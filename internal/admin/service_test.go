package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tempcdn/tempcdn/internal/idgen"
	"github.com/tempcdn/tempcdn/internal/metadata"
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

	// The token itself must never be persisted - only its hash.
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

// TestVerifySessionFailsAndCleansUpExpiredSession guards two behaviors at
// once: an expired session must be rejected, and it must be removed from
// storage as a side effect (rather than lingering to be rejected again on
// every future request).
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

// TestLogoutIsIdempotent guards that logging out an already-revoked or
// never-issued token is not an error - logging out should always leave
// the client in the "logged out" state, not surface a spurious failure.
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
