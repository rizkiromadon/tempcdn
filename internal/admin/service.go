// Package admin implements authentication for the admin dashboard API:
// username/password login backed by bcrypt password hashes, and opaque,
// server-side, revocable sessions (rows in admin_sessions) rather than
// stateless JWTs - see metadata.AdminSession for why.
package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tempcdn/tempcdn/internal/idgen"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned by Login for both "username not found"
// and "wrong password" - never distinguished in the API response, so that
// an attacker probing the login endpoint can't use the error to enumerate
// valid usernames.
var ErrInvalidCredentials = errors.New("invalid username or password")

var ErrSessionInvalid = errors.New("invalid or expired session")

// minPasswordLength is enforced both when creating an admin at bootstrap
// and (if ever exposed) via any future admin-management endpoint.
const minPasswordLength = 12

// sessionTTL is how long a session stays valid after login without further
// use. Re-authenticating simply means logging in again; there is no
// separate "refresh token" concept, since sessions are cheap, revocable
// database rows rather than long-lived JWTs.
const sessionTTL = 24 * time.Hour

type Service struct {
	repository metadata.Repository
	now        func() time.Time
}

func NewService(repository metadata.Repository) *Service {
	return &Service{
		repository: repository,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Session is what callers get back after a successful Login or
// VerifySession: the session record plus which admin it belongs to.
type Session struct {
	Admin   *metadata.Admin
	Session *metadata.AdminSession
}

// LoginResult carries the plaintext session token, which - like a delete
// token - exists only in memory for this one response and is never stored;
// only its hash is persisted (see metadata.AdminSession.TokenHash).
type LoginResult struct {
	Token   string
	Admin   *metadata.Admin
	Session *metadata.AdminSession
}

// Login verifies username/password and, on success, creates a new session
// row and returns its plaintext token. Timing is intentionally
// close-to-constant between the "username not found" and "wrong password"
// cases: a bcrypt comparison always runs, against a real hash when the
// admin exists or a fixed dummy hash when it doesn't, so responses don't
// leak which case occurred through response latency.
func (s *Service) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	acct, err := s.repository.FindAdminByUsername(ctx, username)
	if err != nil && !errors.Is(err, metadata.ErrAdminNotFound) {
		return nil, fmt.Errorf("look up admin: %w", err)
	}

	passwordHash := dummyPasswordHash
	if acct != nil {
		passwordHash = acct.PasswordHash
	}
	bcryptErr := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))

	if acct == nil || bcryptErr != nil {
		return nil, ErrInvalidCredentials
	}

	token, err := idgen.NewSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}

	now := s.now()
	session := &metadata.AdminSession{
		TokenHash:  idgen.HashSessionToken(token),
		AdminID:    acct.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(sessionTTL),
		LastUsedAt: now,
	}
	if err := s.repository.InsertAdminSession(ctx, session); err != nil {
		return nil, fmt.Errorf("persist admin session: %w", err)
	}

	if err := s.repository.TouchAdminLastLogin(ctx, acct.ID, now); err != nil {
		return nil, fmt.Errorf("update admin last login: %w", err)
	}

	return &LoginResult{Token: token, Admin: acct, Session: session}, nil
}

// VerifySession looks up a session by its plaintext token, checking both
// that it exists and that it hasn't expired, and refreshes last_used_at on
// success. Used by the RequireAdminSession middleware on every
// authenticated request.
func (s *Service) VerifySession(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrSessionInvalid
	}
	tokenHash := idgen.HashSessionToken(token)

	session, err := s.repository.FindAdminSessionByTokenHash(ctx, tokenHash)
	if errors.Is(err, metadata.ErrAdminSessionNotFound) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("look up admin session: %w", err)
	}

	now := s.now()
	if session.IsExpired(now) {
		// Best-effort cleanup of this one expired row; failure here doesn't
		// change the outcome (session is invalid either way) so it's not
		// worth failing the whole request over.
		_ = s.repository.DeleteAdminSession(ctx, tokenHash)
		return nil, ErrSessionInvalid
	}

	admin, err := s.repository.FindAdminByID(ctx, session.AdminID)
	if errors.Is(err, metadata.ErrAdminNotFound) {
		// The admin account was deleted after this session was created.
		// Treat the session as invalid rather than surfacing a 500 -
		// clean up the now-orphaned session row while we're here.
		_ = s.repository.DeleteAdminSession(ctx, tokenHash)
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("look up admin by id: %w", err)
	}

	if err := s.repository.TouchAdminSession(ctx, tokenHash, now); err != nil {
		return nil, fmt.Errorf("touch admin session: %w", err)
	}

	return &Session{Admin: admin, Session: session}, nil
}

// Logout revokes a single session. Idempotent: logging out a token that's
// already invalid/expired/unknown is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	tokenHash := idgen.HashSessionToken(token)
	if err := s.repository.DeleteAdminSession(ctx, tokenHash); err != nil {
		return fmt.Errorf("delete admin session: %w", err)
	}
	return nil
}

// dummyPasswordHash is a valid bcrypt hash of an arbitrary fixed string,
// used as the comparison target in Login when no admin account matches
// the given username, so bcrypt.CompareHashAndPassword still does a full
// comparison and takes roughly the same time as the "admin exists but
// password is wrong" path.
const dummyPasswordHash = "$2a$12$CwTycUXWue0Thq9StjUM0uJ8p1M4gk1EYr9y7Wz4qOfNaqCzWn9E."
