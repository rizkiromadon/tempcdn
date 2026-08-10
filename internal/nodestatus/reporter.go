// Package nodestatus lets every tempcdn instance sharing one database
// (e.g. srv1/srv2/srv3 behind a rotating frontend) report its own liveness
// and observe the liveness of the others, entirely through that shared
// database - no gossip protocol or direct server-to-server connectivity is
// required.
//
// Three pieces make this work:
//   - Reporter: runs in every instance, periodically upserting that
//     instance's own row (see metadata.Repository.Heartbeat).
//   - Janitor: runs in every instance too, periodically flipping any row
//     whose heartbeat has gone stale to "offline" (see
//     metadata.Repository.MarkStaleOffline). Any still-live instance can
//     and does do this for any other instance - a node cannot mark itself
//     offline on crash, since it never gets the chance to run that code.
//   - Handler: serves GET /api/v1/nodes, a read-only view of every known
//     node's current row.
package nodestatus

import (
	"context"
	"log/slog"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
)

// Reporter periodically upserts one node's own row in node_status, so other
// instances (via their own Janitor) can tell this node is still alive.
type Reporter struct {
	repository metadata.Repository
	nodeID     string
	hostname   string
	interval   time.Duration
	startedAt  time.Time
	logger     *slog.Logger
	now        func() time.Time
}

func NewReporter(repository metadata.Repository, nodeID, hostname string, interval time.Duration, logger *slog.Logger) *Reporter {
	return &Reporter{
		repository: repository,
		nodeID:     nodeID,
		hostname:   hostname,
		interval:   interval,
		startedAt:  time.Now().UTC(),
		logger:     logger,
		now:        time.Now,
	}
}

// Run blocks, heartbeating every interval until ctx is cancelled. Call this
// in its own goroutine.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Heartbeat immediately on startup rather than waiting a full interval,
	// so this node shows up as online (and any prior "offline" row for the
	// same node ID clears) as soon as possible after a restart.
	r.beat(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.beat(ctx)
		}
	}
}

func (r *Reporter) beat(ctx context.Context) {
	now := r.now().UTC()
	if err := r.repository.Heartbeat(ctx, r.nodeID, r.hostname, r.startedAt, now); err != nil {
		r.logger.Error("nodestatus_heartbeat_failed", "node_id", r.nodeID, "error", err)
	}
}
