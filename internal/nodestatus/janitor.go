package nodestatus

import (
	"context"
	"log/slog"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

type Janitor struct {
	repository metadata.Repository
	interval   time.Duration
	staleAfter time.Duration
	logger     *slog.Logger
	now        func() time.Time
}

func NewJanitor(repository metadata.Repository, interval, staleAfter time.Duration, logger *slog.Logger) *Janitor {
	return &Janitor{
		repository: repository,
		interval:   interval,
		staleAfter: staleAfter,
		logger:     logger,
		now:        time.Now,
	}
}

func (j *Janitor) Run(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	j.sweepOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.sweepOnce(ctx)
		}
	}
}

func (j *Janitor) sweepOnce(ctx context.Context) {
	now := j.now().UTC()
	staleBefore := now.Add(-j.staleAfter)

	offlineNodeIDs, err := j.repository.MarkStaleOffline(ctx, staleBefore, now)
	if err != nil {
		j.logger.Error("nodestatus_janitor_sweep_failed", "error", err)
		return
	}
	if len(offlineNodeIDs) == 0 {
		return
	}
	j.logger.Info("nodestatus_janitor_marked_offline", "node_ids", offlineNodeIDs, "count", len(offlineNodeIDs))
}
