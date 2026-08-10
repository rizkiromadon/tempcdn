// Package sweeper implements the application-level enforcement of file
// expiry. An R2 Lifecycle Rule can additionally be configured on the bucket
// as defense-in-depth, but this package is the primary, verifiable mechanism
// that keeps the "temporary" promise of the CDN: nothing in this repo should
// depend on an external, unchecked dashboard setting for its core behavior.
package sweeper

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tempcdn/tempcdn/internal/cloudflare"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/storage"
)

// batchSize caps how many expired records are processed per tick, so a large
// backlog (e.g. after downtime) doesn't hold a database connection for an
// unbounded amount of time in one query.
const batchSize = 100

type Sweeper struct {
	repository    metadata.Repository
	objectStorage storage.ObjectStorage
	cachePurger   cloudflare.Purger
	cachePurgeOn  bool
	interval      time.Duration
	logger        *slog.Logger
}

func New(repository metadata.Repository, objectStorage storage.ObjectStorage, cachePurger cloudflare.Purger, cachePurgeOn bool, interval time.Duration, logger *slog.Logger) *Sweeper {
	return &Sweeper{
		repository:    repository,
		objectStorage: objectStorage,
		cachePurger:   cachePurger,
		cachePurgeOn:  cachePurgeOn,
		interval:      interval,
		logger:        logger,
	}
}

// Run blocks, sweeping expired files every interval until ctx is cancelled.
// Call this in its own goroutine.
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run an initial sweep immediately on startup rather than waiting a
	// full interval, so files that expired while the server was down
	// don't linger unnecessarily.
	s.sweepOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

func (s *Sweeper) sweepOnce(ctx context.Context) {
	now := time.Now().UTC()

	records, err := s.repository.FindExpired(ctx, now, batchSize)
	if err != nil {
		s.logger.Error("sweeper_find_expired_failed", "error", err)
		return
	}
	if len(records) == 0 {
		return
	}

	var purgeURLs []string
	deletedCount := 0

	for _, record := range records {
		if err := s.objectStorage.DeleteObject(ctx, record.ObjectKey); err != nil {
			s.logger.Error("sweeper_delete_object_failed", "id", record.ID, "object_key", record.ObjectKey, "error", err)
			// Don't delete the DB row if we couldn't confirm the object
			// was removed from R2 - retry on the next tick instead of
			// silently losing track of an object that's still live.
			continue
		}

		if err := s.repository.DeleteByID(ctx, record.ID); err != nil && !errors.Is(err, metadata.ErrFileNotFound) {
			s.logger.Error("sweeper_delete_record_failed", "id", record.ID, "error", err)
			continue
		}

		deletedCount++
		if record.CDNURL != "" {
			purgeURLs = append(purgeURLs, record.CDNURL)
		}
	}

	if s.cachePurgeOn && s.cachePurger != nil && len(purgeURLs) > 0 {
		if err := s.cachePurger.PurgeURLs(ctx, purgeURLs); err != nil {
			s.logger.Error("sweeper_cache_purge_failed", "count", len(purgeURLs), "error", err)
		}
	}

	s.logger.Info("sweeper_tick", "expired_found", len(records), "deleted", deletedCount)
}
