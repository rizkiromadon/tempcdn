package metadata

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrFileNotFound = errors.New("file record not found")

// errInvalidDSN is returned by NewRepository when DATABASE_DSN is not a
// Postgres connection string. tempcdn requires Postgres for every
// deployment (see NewRepository).
var errInvalidDSN = errors.New(`invalid DATABASE_DSN: tempcdn requires Postgres; DATABASE_DSN must start with "postgres://" or "postgresql://"`)

type Repository interface {
	Migrate(ctx context.Context) error
	Insert(ctx context.Context, record *FileRecord) error
	FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*FileRecord, error)
	FindByID(ctx context.Context, id string) (*FileRecord, error)
	DeleteByID(ctx context.Context, id string) error
	// FindExpired returns up to limit records whose expires_at is at or
	// before "before", for the background expiry sweep. Ordered oldest
	// expiry first so the most overdue records are cleaned up first.
	FindExpired(ctx context.Context, before time.Time, limit int) ([]*FileRecord, error)
	// Stats aggregates the files table as of "now": only rows that are
	// currently active (expires_at > now) are counted, since expired/deleted
	// rows are physically removed by the sweeper and DELETE /files/{id} (see
	// DeleteByID) rather than flagged - the table never holds a lifetime
	// history. Callers wanting lifetime totals must combine this with a
	// monotonically-increasing counter recorded at upload time (see
	// httpserver.Metrics), not with this table.
	Stats(ctx context.Context, now time.Time) (*Stats, error)

	// Heartbeat upserts the calling node's own row: creates it on first
	// call (recording startedAt) or, on every later call, refreshes
	// last_heartbeat_at and forces status back to "online" - a node that
	// was flagged offline by another instance's janitor and then comes
	// back reclaims "online" the moment it heartbeats again.
	Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error
	// MarkStaleOffline flips every row still marked "online" whose
	// last_heartbeat_at is at or before "before" to "offline", stamping
	// marked_offline_at, and returns the affected node IDs. Safe to call
	// concurrently from more than one instance's janitor tick: the WHERE
	// status = 'online' guard means a row already flipped by another
	// instance's tick is simply not matched again, not double-counted or
	// errored on.
	MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error)
	// ListNodeStatus returns every known node's row, most recently
	// heartbeated first, for the GET /api/v1/nodes endpoint.
	ListNodeStatus(ctx context.Context) ([]*NodeStatus, error)

	Close() error
}

// Stats holds aggregate figures over the files currently active in the
// table (see Repository.Stats).
type Stats struct {
	ActiveFileCount int64
	ActiveBytes     int64
	// ContentTypeBreakdown maps top-level MIME type (the part before "/",
	// e.g. "image", "video", "application") to the number of active files
	// of that type. An empty or malformed content_type is grouped under
	// "other".
	ContentTypeBreakdown map[string]int64
}

// NewRepository always returns a PostgresRepository: tempcdn requires
// Postgres for every deployment, including a single standalone instance,
// so that metadata storage behaves identically regardless of how many
// instances are running. dsn must be a "postgres://" or "postgresql://"
// connection string. maxConns caps the pool size (see
// NewPostgresRepository) so a single instance doesn't exhaust a managed
// database's connection slot limit.
func NewRepository(ctx context.Context, dsn string, maxConns int32) (Repository, error) {
	if !isPostgresDSN(dsn) {
		return nil, errInvalidDSN
	}
	return NewPostgresRepository(ctx, dsn, maxConns)
}

// topLevelMimeType returns the part of a MIME type before "/" (e.g.
// "image/png" -> "image"), grouping multiple subtypes of the same media
// category together in the breakdown. Empty or malformed values (no "/")
// fall back to "other" rather than being silently dropped or panicking on a
// missing separator.
func topLevelMimeType(contentType string) string {
	slashIndex := strings.IndexByte(contentType, '/')
	if slashIndex <= 0 {
		return "other"
	}
	return contentType[:slashIndex]
}

// isPostgresDSN reports whether dsn looks like a Postgres connection
// string.
func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}
