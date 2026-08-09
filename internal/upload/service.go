package upload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tempcdn/tempcdn/internal/idgen"
	"github.com/tempcdn/tempcdn/internal/metadata"
	"github.com/tempcdn/tempcdn/internal/storage"
)

const sniffBufferSize = 512

type Service struct {
	repository    metadata.Repository
	objectStorage storage.ObjectStorage
	validator     *Validator
	fileTTL       time.Duration
	publicBaseURL string
}

type Input struct {
	OriginalName   string
	SizeBytes      int64
	Content        io.Reader
	UploaderIPHash string
}

type Result struct {
	Record    *metadata.FileRecord
	Duplicate bool
}

func NewService(repository metadata.Repository, objectStorage storage.ObjectStorage, validator *Validator, fileTTL time.Duration, publicBaseURL string) *Service {
	return &Service{
		repository:    repository,
		objectStorage: objectStorage,
		validator:     validator,
		fileTTL:       fileTTL,
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
	}
}

func (s *Service) Upload(ctx context.Context, input Input) (*Result, error) {
	if err := s.validator.ValidateSize(input.SizeBytes); err != nil {
		return nil, err
	}
	if err := s.validator.ValidateExtension(input.OriginalName); err != nil {
		return nil, err
	}

	sniffBuffer := make([]byte, sniffBufferSize)
	bytesRead, err := io.ReadFull(input.Content, sniffBuffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("read file header: %w", err)
	}
	sniffBuffer = sniffBuffer[:bytesRead]

	detectedContentType, err := s.validator.DetectAndValidateContentType(sniffBuffer)
	if err != nil {
		return nil, err
	}

	fullContent := io.MultiReader(bytes.NewReader(sniffBuffer), input.Content)
	checksumReader := NewChecksummingReader(fullContent)

	spooledFile, err := os.CreateTemp("", "tempcdn-upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp spool file: %w", err)
	}
	defer func() {
		_ = spooledFile.Close()
		_ = os.Remove(spooledFile.Name())
	}()

	if _, err := io.Copy(spooledFile, checksumReader); err != nil {
		return nil, fmt.Errorf("spool file content while hashing: %w", err)
	}
	checksum := checksumReader.SumHex()

	now := time.Now().UTC()

	existingRecord, dedupErr := s.repository.FindActiveByChecksum(ctx, checksum, now)
	if dedupErr == nil {
		return &Result{Record: existingRecord, Duplicate: true}, nil
	}
	if dedupErr != metadata.ErrFileNotFound {
		return nil, fmt.Errorf("lookup checksum for dedup: %w", dedupErr)
	}

	if _, err := spooledFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind spool file: %w", err)
	}

	fileID := idgen.NewFileID()
	objectKey := buildObjectKey(now, fileID, input.OriginalName)

	putErr := s.objectStorage.PutObject(ctx, storage.PutObjectInput{
		Key:         objectKey,
		Body:        spooledFile,
		ContentType: detectedContentType,
		SizeBytes:   input.SizeBytes,
	})
	if putErr != nil {
		return nil, fmt.Errorf("upload object to storage: %w", putErr)
	}

	expiresAt := now.Add(s.fileTTL)
	record := &metadata.FileRecord{
		ID:             fileID,
		OriginalName:   input.OriginalName,
		ContentType:    detectedContentType,
		SizeBytes:      input.SizeBytes,
		ChecksumSHA256: checksum,
		ObjectKey:      objectKey,
		CDNURL:         s.publicBaseURL + "/" + objectKey,
		UploaderIPHash: input.UploaderIPHash,
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
	}

	if err := s.repository.Insert(ctx, record); err != nil {
		_ = s.objectStorage.DeleteObject(ctx, objectKey)
		return nil, fmt.Errorf("persist file metadata: %w", err)
	}

	return &Result{Record: record, Duplicate: false}, nil
}

func buildObjectKey(now time.Time, fileID string, originalName string) string {
	extension := filepath.Ext(originalName)
	return fmt.Sprintf("%04d/%02d/%02d/%s%s", now.Year(), now.Month(), now.Day(), fileID, extension)
}
