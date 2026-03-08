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
	MaxSize      int64             // 0 = server default
	AllowedTypes []string          // nil = any type allowed
	Signature    string            // SHA-256 content hash; empty = no uniqueness check
	Scope        string            // uniqueness boundary; empty = global
	Tags         map[string]string // arbitrary key-value tags for token search/audit
}

// PresignGetParams holds parameters for generating a presigned GET URL.
type PresignGetParams struct {
	Bucket   string
	Key      string
	Expires  time.Duration
	Filename string // Content-Disposition attachment filename; empty = none
}

// ListResult holds paginated results from ListByPrefix.
type ListResult struct {
	Keys          []string
	NextPageToken string
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
	PresignGet(ctx context.Context, params PresignGetParams) (string, error)

	// ListByPrefix returns object keys matching the given prefix.
	// When pageSize <= 0, all matching keys are returned.
	// When pageSize > 0, at most pageSize keys are returned with a NextPageToken for continuation.
	ListByPrefix(ctx context.Context, bucket, prefix string, pageSize int, pageToken string) (*ListResult, error)

	// EnsureBucket creates the bucket if it does not already exist.
	EnsureBucket(ctx context.Context, bucket string) error
}
