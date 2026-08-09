package storage

import (
	"context"
	"io"
)

type PutObjectInput struct {
	Key         string
	Body        io.Reader
	ContentType string
	SizeBytes   int64
}

type ObjectStorage interface {
	PutObject(ctx context.Context, input PutObjectInput) error
	DeleteObject(ctx context.Context, key string) error
}
