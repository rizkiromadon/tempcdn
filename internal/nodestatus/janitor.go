package nodestatus

import (
	"context"
	"log/slog"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
)

// Janitor periodically flips any node_status row whose last heartbeat is
// older than staleAfter to "offline". It runs in every instance - there is
// no designated "leader" - which is safe because
// metadata.Repository.MarkStaleOffline only touches rows still marked
// "online" (see its doc comment for why concurrent ticks from different
// instances don't double-flip or race).
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

// Run blocks, sweeping for stale nodes every interval until ctx is
// cancelled. Call this in its own goroutine.
func (j *Janitor) Run(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Sweep immediately on startup too, so nodes that went stale while
	// every other instance was also down get flagged as soon as any
	// instance comes back up, rather than waiting a full interval.
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
