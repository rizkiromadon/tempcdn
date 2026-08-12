package metadata

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrFileNotFound = errors.New("file record not found")

var (
	ErrAdminNotFound        = errors.New("admin not found")
	ErrAdminUsernameTaken   = errors.New("admin username already taken")
	ErrAdminSessionNotFound = errors.New("admin session not found")
)

var ErrAPIKeyNotFound = errors.New("api key not found")

var ErrUploadSettingsNotFound = errors.New("upload settings not found")

var errInvalidDSN = errors.New(`invalid DATABASE_DSN: tempcdn requires Postgres; DATABASE_DSN must start with "postgres://" or "postgresql://"`)

type Repository interface {
	Migrate(ctx context.Context) error
	Insert(ctx context.Context, record *FileRecord) error
	FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*FileRecord, error)
	FindByID(ctx context.Context, id string) (*FileRecord, error)
	DeleteByID(ctx context.Context, id string) error

	FindExpired(ctx context.Context, before time.Time, limit int) ([]*FileRecord, error)

	Stats(ctx context.Context, now time.Time) (*Stats, error)

	Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error

	MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error)

	ListNodeStatus(ctx context.Context) ([]*NodeStatus, error)

	InsertAdmin(ctx context.Context, admin *Admin) error

	FindAdminByUsername(ctx context.Context, username string) (*Admin, error)

	FindAdminByID(ctx context.Context, id string) (*Admin, error)

	CountAdmins(ctx context.Context) (int64, error)

	TouchAdminLastLogin(ctx context.Context, adminID string, now time.Time) error

	InsertAdminSession(ctx context.Context, session *AdminSession) error

	FindAdminSessionByTokenHash(ctx context.Context, tokenHash string) (*AdminSession, error)

	TouchAdminSession(ctx context.Context, tokenHash string, now time.Time) error

	DeleteAdminSession(ctx context.Context, tokenHash string) error

	DeleteExpiredAdminSessions(ctx context.Context, before time.Time) error

	InsertAPIKey(ctx context.Context, key *APIKey) error

	FindAPIKeyByTokenHash(ctx context.Context, tokenHash string) (*APIKey, error)

	ListAPIKeys(ctx context.Context) ([]*APIKey, error)

	TouchAPIKey(ctx context.Context, id string, now time.Time) error

	RevokeAPIKey(ctx context.Context, id string, now time.Time) error

	GetUploadSettings(ctx context.Context) (*UploadSettings, error)

	SeedUploadSettingsIfMissing(ctx context.Context, settings *UploadSettings) error

	UpdateUploadSettings(ctx context.Context, settings *UploadSettings, updatedBy string, now time.Time) error

	Close() error
}

type Stats struct {
	ActiveFileCount int64
	ActiveBytes     int64

	ContentTypeBreakdown map[string]int64
}

func NewRepository(ctx context.Context, dsn string, maxConns int32) (Repository, error) {
	if !isPostgresDSN(dsn) {
		return nil, errInvalidDSN
	}
	return NewPostgresRepository(ctx, dsn, maxConns)
}

func topLevelMimeType(contentType string) string {
	slashIndex := strings.IndexByte(contentType, '/')
	if slashIndex <= 0 {
		return "other"
	}
	return contentType[:slashIndex]
}

func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}
