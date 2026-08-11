package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
)

// sessionJanitorInterval is how often expired admin_sessions rows are
// purged. Expired sessions are already rejected by VerifySession (and
// opportunistically deleted one-at-a-time there), so this is just periodic
// bulk cleanup to keep the table from growing unbounded from sessions that
// were never touched again after expiring.
const sessionJanitorInterval = 1 * time.Hour

// SessionJanitor periodically deletes expired admin_sessions rows.
type SessionJanitor struct {
	repository metadata.Repository
	logger     *slog.Logger
}

func NewSessionJanitor(repository metadata.Repository, logger *slog.Logger) *SessionJanitor {
	return &SessionJanitor{repository: repository, logger: logger}
}

// Run blocks, sweeping expired sessions every sessionJanitorInterval until
// ctx is cancelled. Call this in its own goroutine.
func (j *SessionJanitor) Run(ctx context.Context) {
	ticker := time.NewTicker(sessionJanitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := j.repository.DeleteExpiredAdminSessions(ctx, time.Now().UTC()); err != nil {
				j.logger.Error("admin_session_janitor_failed", "error", err)
			}
		}
	}
}
