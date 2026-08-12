
package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/idgen"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid username or password")

var ErrSessionInvalid = errors.New("invalid or expired session")

var ErrAPIKeyInvalid = errors.New("invalid or revoked api key")

const minPasswordLength = 12

const sessionTTL = 24 * time.Hour

// ServiceRepository is everything admin.Service needs to persist. It is a
// composition of the narrow domain interfaces from package metadata, kept
// local to this package so the dependency is explicit and easy to fake in
// tests without pulling in unrelated repository methods (files, nodes...).
type ServiceRepository interface {
	metadata.AdminRepository
	metadata.APIKeyRepository
	metadata.UploadSettingsRepository
}

type Service struct {
	repository ServiceRepository
	now        func() time.Time
}

func NewService(repository ServiceRepository) *Service {
	return &Service{
		repository: repository,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

type Session struct {
	Admin   *metadata.Admin
	Session *metadata.AdminSession
}

type LoginResult struct {
	Token   string
	Admin   *metadata.Admin
	Session *metadata.AdminSession
}

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

		_ = s.repository.DeleteAdminSession(ctx, tokenHash)
		return nil, ErrSessionInvalid
	}

	admin, err := s.repository.FindAdminByID(ctx, session.AdminID)
	if errors.Is(err, metadata.ErrAdminNotFound) {

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

type CreateAPIKeyResult struct {
	Key    string
	Record *metadata.APIKey
}

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

func (s *Service) ListAPIKeys(ctx context.Context) ([]*metadata.APIKey, error) {
	keys, err := s.repository.ListAPIKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, id string) error {
	if err := s.repository.RevokeAPIKey(ctx, id, s.now()); err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

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

var ErrInvalidUploadSettings = errors.New("invalid upload settings")

const maxUploadSizeMBCeiling = 10240

func (s *Service) GetUploadSettings(ctx context.Context) (*metadata.UploadSettings, error) {
	settings, err := s.repository.GetUploadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get upload settings: %w", err)
	}
	return settings, nil
}

type UpdateUploadSettingsInput struct {
	MaxUploadSizeMB   int64
	AllowedMimeTypes  []string
	BlockedExtensions []string
}

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

const dummyPasswordHash = "$2a$12$CwTycUXWue0Thq9StjUM0uJ8p1M4gk1EYr9y7Wz4qOfNaqCzWn9E."
