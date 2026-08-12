package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

const sessionJanitorInterval = 1 * time.Hour

type SessionJanitor struct {
	repository metadata.AdminRepository
	logger     *slog.Logger
}

func NewSessionJanitor(repository metadata.AdminRepository, logger *slog.Logger) *SessionJanitor {
	return &SessionJanitor{repository: repository, logger: logger}
}

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
