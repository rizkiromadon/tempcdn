package upload

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/storage"
)

type mockObjectStorage struct {
	putObjectCallCount    int
	deleteObjectCallCount int
	putObjectErr          error
}

func (m *mockObjectStorage) PutObject(ctx context.Context, input storage.PutObjectInput) error {
	m.putObjectCallCount++
	if m.putObjectErr != nil {
		return m.putObjectErr
	}
	return nil
}

func (m *mockObjectStorage) DeleteObject(ctx context.Context, key string) error {
	m.deleteObjectCallCount++
	return nil
}

type mockRepository struct {
	recordsByChecksum map[string]*metadata.FileRecord
	insertedRecords   []*metadata.FileRecord
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		recordsByChecksum: make(map[string]*metadata.FileRecord),
	}
}

func (m *mockRepository) Migrate(ctx context.Context) error { return nil }

func (m *mockRepository) Insert(ctx context.Context, record *metadata.FileRecord) error {
	m.recordsByChecksum[record.ChecksumSHA256] = record
	m.insertedRecords = append(m.insertedRecords, record)
	return nil
}

func (m *mockRepository) FindActiveByChecksum(ctx context.Context, checksum string, now time.Time) (*metadata.FileRecord, error) {
	record, exists := m.recordsByChecksum[checksum]
	if !exists || record.IsExpired(now) {
		return nil, metadata.ErrFileNotFound
	}
	return record, nil
}

func (m *mockRepository) FindByID(ctx context.Context, id string) (*metadata.FileRecord, error) {
	for _, record := range m.recordsByChecksum {
		if record.ID == id {
			return record, nil
		}
	}
	return nil, metadata.ErrFileNotFound
}

func (m *mockRepository) FindExpired(ctx context.Context, before time.Time, limit int) ([]*metadata.FileRecord, error) {
	var expired []*metadata.FileRecord
	for _, record := range m.recordsByChecksum {
		if !record.ExpiresAt.After(before) {
			expired = append(expired, record)
		}
		if len(expired) >= limit {
			break
		}
	}
	return expired, nil
}

func (m *mockRepository) DeleteByID(ctx context.Context, id string) error {
	return nil
}

func (m *mockRepository) Stats(ctx context.Context, now time.Time) (*metadata.Stats, error) {
	return &metadata.Stats{ContentTypeBreakdown: map[string]int64{}}, nil
}

func (m *mockRepository) Heartbeat(ctx context.Context, nodeID, hostname string, startedAt, now time.Time) error {
	return nil
}
func (m *mockRepository) MarkStaleOffline(ctx context.Context, before, now time.Time) ([]string, error) {
	return nil, nil
}
func (m *mockRepository) ListNodeStatus(ctx context.Context) ([]*metadata.NodeStatus, error) {
	return nil, nil
}
func (m *mockRepository) Close() error { return nil }

func TestUploadServiceFirstUploadStoresObjectAndMetadata(t *testing.T) {
	repo := newMockRepository()
	objectStorage := &mockObjectStorage{}
	validator := NewValidator(1024*1024, []string{"image/*"}, nil)
	service := NewService(repo, objectStorage, validator, 24*time.Hour, "https://cdn.tempcdn.example.com")

	pngContent := buildMinimalPNGContent()

	result, err := service.Upload(context.Background(), Input{
		OriginalName:   "photo.png",
		SizeBytes:      int64(len(pngContent)),
		Content:        bytes.NewReader(pngContent),
		UploaderIPHash: "hashed-ip",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Duplicate {
		t.Error("expected first upload to not be a duplicate")
	}
	if objectStorage.putObjectCallCount != 1 {
		t.Errorf("expected exactly 1 PutObject call, got %d", objectStorage.putObjectCallCount)
	}
	if len(repo.insertedRecords) != 1 {
		t.Errorf("expected exactly 1 inserted record, got %d", len(repo.insertedRecords))
	}
}

func TestUploadServiceDuplicateUploadSkipsPutObject(t *testing.T) {
	repo := newMockRepository()
	objectStorage := &mockObjectStorage{}
	validator := NewValidator(1024*1024, []string{"image/*"}, nil)
	service := NewService(repo, objectStorage, validator, 24*time.Hour, "https://cdn.tempcdn.example.com")

	pngContent := buildMinimalPNGContent()

	firstResult, err := service.Upload(context.Background(), Input{
		OriginalName:   "photo.png",
		SizeBytes:      int64(len(pngContent)),
		Content:        bytes.NewReader(pngContent),
		UploaderIPHash: "hashed-ip",
	})
	if err != nil {
		t.Fatalf("first upload failed: %v", err)
	}

	secondResult, err := service.Upload(context.Background(), Input{
		OriginalName:   "photo-again.png",
		SizeBytes:      int64(len(pngContent)),
		Content:        bytes.NewReader(pngContent),
		UploaderIPHash: "hashed-ip-2",
	})
	if err != nil {
		t.Fatalf("second upload failed: %v", err)
	}

	if !secondResult.Duplicate {
		t.Error("expected second upload to be flagged as duplicate")
	}
	if objectStorage.putObjectCallCount != 1 {
		t.Errorf("expected PutObject to be called exactly once across both uploads, got %d", objectStorage.putObjectCallCount)
	}
	if len(repo.insertedRecords) != 1 {
		t.Errorf("expected only 1 record inserted, got %d", len(repo.insertedRecords))
	}
	if secondResult.Record.ID != firstResult.Record.ID {
		t.Error("expected duplicate result to reference the original record's ID")
	}
	if secondResult.Record.ObjectKey != firstResult.Record.ObjectKey {
		t.Error("expected duplicate result to reference the original object key")
	}
}

func TestUploadServiceExpiredDuplicateTriggersFreshUpload(t *testing.T) {
	repo := newMockRepository()
	objectStorage := &mockObjectStorage{}
	validator := NewValidator(1024*1024, []string{"image/*"}, nil)
	service := NewService(repo, objectStorage, validator, 24*time.Hour, "https://cdn.tempcdn.example.com")

	pngContent := buildMinimalPNGContent()

	firstResult, err := service.Upload(context.Background(), Input{
		OriginalName:   "photo.png",
		SizeBytes:      int64(len(pngContent)),
		Content:        bytes.NewReader(pngContent),
		UploaderIPHash: "hashed-ip",
	})
	if err != nil {
		t.Fatalf("first upload failed: %v", err)
	}

	firstResult.Record.ExpiresAt = time.Now().UTC().Add(-1 * time.Hour)
	repo.recordsByChecksum[firstResult.Record.ChecksumSHA256] = firstResult.Record

	secondResult, err := service.Upload(context.Background(), Input{
		OriginalName:   "photo.png",
		SizeBytes:      int64(len(pngContent)),
		Content:        bytes.NewReader(pngContent),
		UploaderIPHash: "hashed-ip",
	})
	if err != nil {
		t.Fatalf("second upload failed: %v", err)
	}

	if secondResult.Duplicate {
		t.Error("expected upload after expiry to not be treated as duplicate")
	}
	if objectStorage.putObjectCallCount != 2 {
		t.Errorf("expected PutObject called twice (original + re-upload after expiry), got %d", objectStorage.putObjectCallCount)
	}
}

func TestUploadServiceRejectsDisallowedExtension(t *testing.T) {
	repo := newMockRepository()
	objectStorage := &mockObjectStorage{}
	validator := NewValidator(1024*1024, []string{"image/*"}, []string{".exe"})
	service := NewService(repo, objectStorage, validator, 24*time.Hour, "https://cdn.tempcdn.example.com")

	_, err := service.Upload(context.Background(), Input{
		OriginalName:   "virus.exe",
		SizeBytes:      100,
		Content:        bytes.NewReader(make([]byte, 100)),
		UploaderIPHash: "hashed-ip",
	})
	if err == nil {
		t.Error("expected error for blocked extension, got nil")
	}
	if objectStorage.putObjectCallCount != 0 {
		t.Error("expected PutObject to not be called for rejected upload")
	}
}

func buildMinimalPNGContent() []byte {
	header := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	padding := bytes.Repeat([]byte{0x00}, 100)
	return append(header, padding...)
}
