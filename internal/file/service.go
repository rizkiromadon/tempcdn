package file

import (
	"context"
	"errors"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/storage"
)

var ErrNotFound = errors.New("file not found")
var ErrAlreadyExpired = errors.New("file has already expired")

type Service struct {
	repository    metadata.Repository
	objectStorage storage.ObjectStorage
}

func NewService(repository metadata.Repository, objectStorage storage.ObjectStorage) *Service {
	return &Service{
		repository:    repository,
		objectStorage: objectStorage,
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

func (s *Service) DeleteBeforeTTL(ctx context.Context, id string) error {
	record, err := s.repository.FindByID(ctx, id)
	if errors.Is(err, metadata.ErrFileNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if record.IsExpired(time.Now().UTC()) {
		return ErrAlreadyExpired
	}

	_ = s.objectStorage.DeleteObject(ctx, record.ObjectKey)

	if err := s.repository.DeleteByID(ctx, id); err != nil && !errors.Is(err, metadata.ErrFileNotFound) {
		return err
	}
	return nil
}
