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

	"github.com/rizkiromadon/tempcdn/internal/idgen"
	"github.com/rizkiromadon/tempcdn/internal/metadata"
	"github.com/rizkiromadon/tempcdn/internal/storage"
)

const sniffBufferSize = 512

type Service struct {
	repository    metadata.FileRepository
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

	DeleteToken string
}

func NewService(repository metadata.FileRepository, objectStorage storage.ObjectStorage, validator *Validator, fileTTL time.Duration, publicBaseURL string) *Service {
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

	writtenBytes, err := io.Copy(spooledFile, checksumReader)
	if err != nil {
		return nil, fmt.Errorf("spool file content while hashing: %w", err)
	}

	if writtenBytes != input.SizeBytes {
		return nil, fmt.Errorf("upload size mismatch: multipart header reported %d bytes but %d bytes were actually read", input.SizeBytes, writtenBytes)
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

	deleteToken, err := idgen.NewDeleteToken()
	if err != nil {
		return nil, fmt.Errorf("generate delete token: %w", err)
	}

	putErr := s.objectStorage.PutObject(ctx, storage.PutObjectInput{
		Key:         objectKey,
		Body:        spooledFile,
		ContentType: detectedContentType,
		SizeBytes:   writtenBytes,
	})
	if putErr != nil {
		return nil, fmt.Errorf("upload object to storage: %w", putErr)
	}

	expiresAt := now.Add(s.fileTTL)
	record := &metadata.FileRecord{
		ID:              fileID,
		OriginalName:    input.OriginalName,
		ContentType:     detectedContentType,
		SizeBytes:       writtenBytes,
		ChecksumSHA256:  checksum,
		ObjectKey:       objectKey,
		CDNURL:          s.publicBaseURL + "/" + objectKey,
		UploaderIPHash:  input.UploaderIPHash,
		DeleteTokenHash: idgen.HashDeleteToken(deleteToken),
		CreatedAt:       now,
		ExpiresAt:       expiresAt,
	}

	if err := s.repository.Insert(ctx, record); err != nil {
		_ = s.objectStorage.DeleteObject(ctx, objectKey)
		return nil, fmt.Errorf("persist file metadata: %w", err)
	}

	return &Result{Record: record, Duplicate: false, DeleteToken: deleteToken}, nil
}

func buildObjectKey(now time.Time, fileID string, originalName string) string {
	extension := filepath.Ext(originalName)
	return fmt.Sprintf("%04d/%02d/%02d/%s%s", now.Year(), now.Month(), now.Day(), fileID, extension)
}
