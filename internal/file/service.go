package file

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/rizkiromadon/tempcdn/internal/cloudflare"
	"github.com/rizkiromadon/tempcdn/internal/idgen"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/storage"
)

var ErrNotFound = errors.New("file not found")
var ErrAlreadyExpired = errors.New("file has already expired")
var ErrInvalidDeleteToken = errors.New("invalid or missing delete token")

type Service struct {
	repository    metadata.Repository
	objectStorage storage.ObjectStorage
	cachePurger   cloudflare.Purger
	cachePurgeOn  bool
	logger        *slog.Logger
}

func NewService(repository metadata.Repository, objectStorage storage.ObjectStorage, cachePurger cloudflare.Purger, cachePurgeOn bool, logger *slog.Logger) *Service {
	return &Service{
		repository:    repository,
		objectStorage: objectStorage,
		cachePurger:   cachePurger,
		cachePurgeOn:  cachePurgeOn,
		logger:        logger,
	}
}

type Info struct {
	Record  *metadata.FileRecord
	Expired bool
}

func (s *Service) GetInfo(ctx context.Context, id string) (*Info, error) {
	record, err := s.repository.FindByID(ctx, id)
	if errors.Is(err, metadata.ErrFileNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &Info{
		Record:  record,
		Expired: record.IsExpired(time.Now().UTC()),
	}, nil
}

func (s *Service) DeleteBeforeTTL(ctx context.Context, id string, deleteToken string) error {
	record, err := s.repository.FindByID(ctx, id)
	if errors.Is(err, metadata.ErrFileNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if !idgen.DeleteTokenMatches(deleteToken, record.DeleteTokenHash) {
		return ErrInvalidDeleteToken
	}

	if record.IsExpired(time.Now().UTC()) {
		return ErrAlreadyExpired
	}

	_ = s.objectStorage.DeleteObject(ctx, record.ObjectKey)

	if err := s.repository.DeleteByID(ctx, id); err != nil && !errors.Is(err, metadata.ErrFileNotFound) {
		return err
	}

	if s.cachePurgeOn && s.cachePurger != nil && record.CDNURL != "" {
		if err := s.cachePurger.PurgeURLs(ctx, []string{record.CDNURL}); err != nil && s.logger != nil {
			s.logger.Error("cloudflare_cache_purge_failed", "id", id, "url", record.CDNURL, "error", err)
		}
	}

	return nil
}
