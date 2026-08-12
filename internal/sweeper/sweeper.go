
package sweeper

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/cloudflare"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/storage"
)

const batchSize = 100

type Sweeper struct {
	repository    metadata.FileRepository
	objectStorage storage.ObjectStorage
	cachePurger   cloudflare.Purger
	cachePurgeOn  bool
	interval      time.Duration
	logger        *slog.Logger
}

func New(repository metadata.FileRepository, objectStorage storage.ObjectStorage, cachePurger cloudflare.Purger, cachePurgeOn bool, interval time.Duration, logger *slog.Logger) *Sweeper {
	return &Sweeper{
		repository:    repository,
		objectStorage: objectStorage,
		cachePurger:   cachePurger,
		cachePurgeOn:  cachePurgeOn,
		interval:      interval,
		logger:        logger,
	}
}

func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

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
