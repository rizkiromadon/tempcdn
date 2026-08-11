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

var ErrAPIKeyInvalid = errors.New("invalid or revoked api key")

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

// CreateAPIKeyResult carries the plaintext API key, which - like a
// session token - exists only in memory for this one response and is
// never stored; only its hash is persisted (see metadata.APIKey.TokenHash).
type CreateAPIKeyResult struct {
	Key    string
	Record *metadata.APIKey
}

// CreateAPIKey generates a new API key, persists its hash, and returns the
// plaintext key. The plaintext is shown to the caller exactly once, in
// this response - it cannot be retrieved again afterward, only revoked and
// replaced with a new key.
func (s *Service) CreateAPIKey(ctx context.Context, name string) (*CreateAPIKeyResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("api key name must not be empty")
	}

	key, err := idgen.NewAPIKey()
	if err != nil {
		return nil, fmt.Errorf("generate api key: %w", err)
	}

	record := &metadata.APIKey{
		ID:        idgen.NewAdminID(),
		Name:      name,
		TokenHash: idgen.HashAPIKey(key),
		CreatedAt: s.now(),
	}
	if err := s.repository.InsertAPIKey(ctx, record); err != nil {
		return nil, fmt.Errorf("persist api key: %w", err)
	}

	return &CreateAPIKeyResult{Key: key, Record: record}, nil
}

// ListAPIKeys returns every API key (active and revoked) for the admin
// dashboard's key management view. Only metadata is returned - the
// plaintext key itself is never retrievable after creation.
func (s *Service) ListAPIKeys(ctx context.Context) ([]*metadata.APIKey, error) {
	keys, err := s.repository.ListAPIKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

// RevokeAPIKey revokes a single API key by ID. Idempotent: revoking an
// already-revoked or nonexistent key is not an error.
func (s *Service) RevokeAPIKey(ctx context.Context, id string) error {
	if err := s.repository.RevokeAPIKey(ctx, id, s.now()); err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

// VerifyAPIKey looks up an API key by its plaintext value, checking both
// that it exists and that it hasn't been revoked, and refreshes
// last_used_at on success. Used by the RequireAPIKeyOrAdminSession
// middleware (or any caller gating server-to-server access, e.g.
// /metrics) as an alternative to a logged-in admin session.
func (s *Service) VerifyAPIKey(ctx context.Context, key string) (*metadata.APIKey, error) {
	if key == "" {
		return nil, ErrAPIKeyInvalid
	}
	tokenHash := idgen.HashAPIKey(key)

	record, err := s.repository.FindAPIKeyByTokenHash(ctx, tokenHash)
	if errors.Is(err, metadata.ErrAPIKeyNotFound) {
		return nil, ErrAPIKeyInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("look up api key: %w", err)
	}

	if record.IsRevoked() {
		return nil, ErrAPIKeyInvalid
	}

	if err := s.repository.TouchAPIKey(ctx, record.ID, s.now()); err != nil {
		return nil, fmt.Errorf("touch api key: %w", err)
	}

	return record, nil
}

// ErrInvalidUploadSettings is returned by UpdateUploadSettings when the
// requested values fail basic sanity checks (see validateUploadSettings).
// Wrapped with %w so a caller wanting the specific message can still
// unwrap it, while errors.Is(err, ErrInvalidUploadSettings) works for a
// simple "was this the client's fault" check.
var ErrInvalidUploadSettings = errors.New("invalid upload settings")

// maxUploadSizeMBCeiling is a hard sanity ceiling on max_upload_size_mb,
// independent of whatever an admin might mistakenly type into the
// dashboard - e.g. protects against a stray extra digit (100 -> 1000000)
// silently pinning every instance's memory/disk spooling to an
// unreasonable size. 10 GiB comfortably covers real-world CDN use cases
// (large video/archive uploads) while still catching typos.
const maxUploadSizeMBCeiling = 10240

// GetUploadSettings returns the current runtime-configurable upload
// limits (max size, allowed MIME types, blocked extensions), for the
// admin dashboard's settings view.
func (s *Service) GetUploadSettings(ctx context.Context) (*metadata.UploadSettings, error) {
	settings, err := s.repository.GetUploadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get upload settings: %w", err)
	}
	return settings, nil
}

// UpdateUploadSettingsInput carries the fields an admin can change. All
// three are required (not partial/PATCH-style) so a caller can't
// accidentally end up with, say, an empty MIME allowlist by omitting the
// field - the admin dashboard is expected to always submit the full
// current settings, pre-filled from a prior GetUploadSettings call, with
// the fields they want to change edited.
type UpdateUploadSettingsInput struct {
	MaxUploadSizeMB   int64
	AllowedMimeTypes  []string
	BlockedExtensions []string
}

// UpdateUploadSettings validates and persists new upload limits, taking
// effect immediately (the caller, internal/httpserver's admin handler, is
// responsible for also calling Validator.Update with the same values so
// in-memory validation matches the database - see admin.Handler.
// UpdateUploadSettings). adminID is recorded as updated_by for
// accountability.
func (s *Service) UpdateUploadSettings(ctx context.Context, adminID string, input UpdateUploadSettingsInput) (*metadata.UploadSettings, error) {
	normalized, err := validateUploadSettings(input)
	if err != nil {
		return nil, err
	}

	now := s.now()
	settings := &metadata.UploadSettings{
		MaxUploadSizeMB:   normalized.MaxUploadSizeMB,
		AllowedMimeTypes:  normalized.AllowedMimeTypes,
		BlockedExtensions: normalized.BlockedExtensions,
		UpdatedAt:         now,
		UpdatedBy:         &adminID,
	}
	if err := s.repository.UpdateUploadSettings(ctx, settings, adminID, now); err != nil {
		return nil, fmt.Errorf("update upload settings: %w", err)
	}
	return settings, nil
}

// validateUploadSettings checks and normalizes an UpdateUploadSettingsInput.
// Trims whitespace and drops empty entries from both list fields (the same
// leniency config.splitAndTrim gave the old ALLOWED_MIME_TYPES/
// BLOCKED_EXTENSIONS environment variables), and requires:
//   - a positive max size within maxUploadSizeMBCeiling
//   - at least one allowed MIME pattern (an empty allowlist would silently
//     reject every upload, which is never the admin's intent even if they
//     forgot to fill it in)
//   - every blocked extension starting with "." (a bare "exe" would never
//     match filepath.Ext's output, silently making that entry a no-op)
//
// Blocked extensions may legitimately be empty (an operator choosing to
// rely on MIME allowlisting alone), so that list has no minimum-length
// check.
func validateUploadSettings(input UpdateUploadSettingsInput) (UpdateUploadSettingsInput, error) {
	if input.MaxUploadSizeMB <= 0 {
		return UpdateUploadSettingsInput{}, fmt.Errorf("%w: max_upload_size_mb must be positive", ErrInvalidUploadSettings)
	}
	if input.MaxUploadSizeMB > maxUploadSizeMBCeiling {
		return UpdateUploadSettingsInput{}, fmt.Errorf("%w: max_upload_size_mb must not exceed %d", ErrInvalidUploadSettings, maxUploadSizeMBCeiling)
	}

	allowedMimeTypes := normalizeStringList(input.AllowedMimeTypes)
	if len(allowedMimeTypes) == 0 {
		return UpdateUploadSettingsInput{}, fmt.Errorf("%w: allowed_mime_types must not be empty", ErrInvalidUploadSettings)
	}

	blockedExtensions := normalizeStringList(input.BlockedExtensions)
	for _, ext := range blockedExtensions {
		if !strings.HasPrefix(ext, ".") {
			return UpdateUploadSettingsInput{}, fmt.Errorf("%w: blocked extension %q must start with \".\"", ErrInvalidUploadSettings, ext)
		}
	}

	return UpdateUploadSettingsInput{
		MaxUploadSizeMB:   input.MaxUploadSizeMB,
		AllowedMimeTypes:  allowedMimeTypes,
		BlockedExtensions: blockedExtensions,
	}, nil
}

// normalizeStringList trims whitespace from every entry and drops any
// that end up empty, without deduplicating (an admin listing the same
// entry twice is harmless, not worth rejecting).
func normalizeStringList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// dummyPasswordHash is a valid bcrypt hash of an arbitrary fixed string,
// used as the comparison target in Login when no admin account matches
// the given username, so bcrypt.CompareHashAndPassword still does a full
// comparison and takes roughly the same time as the "admin exists but
// password is wrong" path.
const dummyPasswordHash = "$2a$12$CwTycUXWue0Thq9StjUM0uJ8p1M4gk1EYr9y7Wz4qOfNaqCzWn9E."
