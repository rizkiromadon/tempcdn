
package nodestatus

import (
	"context"
	"log/slog"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/metadata"
)

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

func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

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
