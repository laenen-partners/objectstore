// Package objectstore defines a storage interface for binary objects (files)
// with presigned URL support for direct client uploads/downloads.
package objectstore

import (
	"context"
	"io"
	"time"
)

// ObjectMeta holds metadata about a stored object.
type ObjectMeta struct {
	Key          string
	Size         int64
	ContentType  string
	LastModified time.Time
	ETag         string
}

// PresignPutParams holds parameters for generating a presigned PUT URL.
type PresignPutParams struct {
	Bucket       string
	Key          string
	ContentType  string
	Expires      time.Duration
	MaxSize      int64    // 0 = server default
	AllowedTypes []string // nil = any type allowed
	Signature    string   // SHA-256 content hash; empty = no uniqueness check
	Scope        string   // uniqueness boundary; empty = global
}

// Store is the interface that object store backends must implement.
type Store interface {
	// PutObject writes data to the given bucket/key.
	PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error

	// GetObject returns a reader for the object at bucket/key.
	// The caller must close the returned ReadCloser.
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)

	// HeadObject returns metadata without fetching the body.
	HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error)

	// DeleteObject removes an object.
	DeleteObject(ctx context.Context, bucket, key string) error

	// PresignPut returns a URL that allows an unauthenticated HTTP PUT
	// to upload an object directly to the store.
	PresignPut(ctx context.Context, params PresignPutParams) (string, error)

	// PresignGet returns a URL that allows an unauthenticated HTTP GET
	// to download an object directly from the store.
	PresignGet(ctx context.Context, bucket, key string, expires time.Duration) (string, error)

	// ListByPrefix returns object keys matching the given prefix.
	ListByPrefix(ctx context.Context, bucket, prefix string) ([]string, error)

	// EnsureBucket creates the bucket if it does not already exist.
	EnsureBucket(ctx context.Context, bucket string) error
}
