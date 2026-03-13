package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/laenen-partners/objectstore/tokenstore"
	pgvalidator "github.com/laenen-partners/objectstore/tokenstore/postgres"
)

// ObjectStore is the main entry point for object storage operations.
// Create one with New and the With* option functions.
type ObjectStore struct {
	store           Store
	tokens          tokenstore.TokenValidator
	pgValidator     *pgvalidator.Validator
	cleanupCancel   context.CancelFunc
	maxExpires      time.Duration
}

type options struct {
	// Backend
	backendType string // "local" or "s3"
	basePath    string
	baseURL     string
	s3Region    string
	s3Endpoint  string

	// Token store
	pgxPool     *pgxpool.Pool
	autoMigrate bool

	// Behavior
	cleanupInterval time.Duration
	maxExpires      time.Duration
}

// Option configures an ObjectStore.
type Option func(*options)

// WithLocalBackend configures a local filesystem backend.
// basePath is the root directory for stored objects.
// baseURL is the public URL prefix for presigned URLs (e.g. "http://localhost:3000").
func WithLocalBackend(basePath, baseURL string) Option {
	return func(o *options) {
		o.backendType = "local"
		o.basePath = basePath
		o.baseURL = baseURL
	}
}

// WithS3Backend configures an AWS S3 or S3-compatible (MinIO) backend.
// region is the AWS region. endpoint is an optional custom endpoint for MinIO (empty for real S3).
func WithS3Backend(region, endpoint string) Option {
	return func(o *options) {
		o.backendType = "s3"
		o.s3Region = region
		o.s3Endpoint = endpoint
	}
}

// WithPgxPool sets the pgx connection pool for the token store.
func WithPgxPool(pool *pgxpool.Pool) Option {
	return func(o *options) {
		o.pgxPool = pool
	}
}

// WithAutoMigrate enables automatic database schema migration on startup.
func WithAutoMigrate() Option {
	return func(o *options) {
		o.autoMigrate = true
	}
}

// WithCleanupInterval sets how often expired tokens are cleaned up.
// Default: 1 hour. Set to 0 to disable automatic cleanup.
func WithCleanupInterval(d time.Duration) Option {
	return func(o *options) {
		o.cleanupInterval = d
	}
}

// WithMaxExpires caps the maximum presigned URL lifetime.
// Default: no cap.
func WithMaxExpires(d time.Duration) Option {
	return func(o *options) {
		o.maxExpires = d
	}
}

// New creates an ObjectStore with the given options.
func New(ctx context.Context, opts ...Option) (*ObjectStore, error) {
	cfg := &options{
		cleanupInterval: 1 * time.Hour,
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.backendType == "" {
		return nil, errors.New("objectstore: backend is required (use WithLocalBackend or WithS3Backend)")
	}

	// Build token validator from pgx pool.
	var tokens tokenstore.TokenValidator
	var pgv *pgvalidator.Validator
	if cfg.pgxPool != nil {
		var pgOpts []pgvalidator.Option
		if cfg.autoMigrate {
			pgOpts = append(pgOpts, pgvalidator.WithMigrations())
		}
		var err error
		pgv, err = pgvalidator.NewFromPool(ctx, cfg.pgxPool, pgOpts...)
		if err != nil {
			return nil, fmt.Errorf("objectstore: create token validator: %w", err)
		}
		tokens = pgv
	}

	// Build store backend.
	var store Store
	switch cfg.backendType {
	case "local":
		if tokens == nil {
			return nil, errors.New("objectstore: WithPgxPool is required for local backend (presigned URLs need token validation)")
		}
		ls, err := NewLocalStore(cfg.basePath, cfg.baseURL, tokens)
		if err != nil {
			return nil, fmt.Errorf("objectstore: create local store: %w", err)
		}
		store = ls
	case "s3":
		s3Store, err := newS3FromOptions(cfg)
		if err != nil {
			return nil, fmt.Errorf("objectstore: create s3 store: %w", err)
		}
		store = s3Store
	}

	os := &ObjectStore{
		store:       store,
		tokens:      tokens,
		pgValidator: pgv,
		maxExpires:  cfg.maxExpires,
	}

	// Start background cleanup if configured.
	if pgv != nil && cfg.cleanupInterval > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		os.cleanupCancel = cancel
		pgv.StartCleanup(ctx, cfg.cleanupInterval)
	}

	return os, nil
}

func newS3FromOptions(cfg *options) (*S3Store, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.s3Region),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.s3Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.s3Endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)
	return NewS3Store(client), nil
}

// Close releases resources held by the ObjectStore.
// Stops the background token cleanup goroutine if running.
// Does not close the pgx pool (the caller owns it).
func (o *ObjectStore) Close() error {
	if o.cleanupCancel != nil {
		o.cleanupCancel()
	}
	return nil
}

// PutObject writes data to the given bucket/key.
func (o *ObjectStore) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	return o.store.PutObject(ctx, bucket, key, r, size, contentType)
}

// GetObject returns a reader for the object at bucket/key.
// The caller must close the returned ReadCloser.
func (o *ObjectStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	return o.store.GetObject(ctx, bucket, key)
}

// HeadObject returns metadata without fetching the body.
func (o *ObjectStore) HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error) {
	return o.store.HeadObject(ctx, bucket, key)
}

// DeleteObject removes an object.
func (o *ObjectStore) DeleteObject(ctx context.Context, bucket, key string) error {
	return o.store.DeleteObject(ctx, bucket, key)
}

// ListByPrefix returns object keys matching the given prefix.
func (o *ObjectStore) ListByPrefix(ctx context.Context, bucket, prefix string, pageSize int, pageToken string) (*ListResult, error) {
	return o.store.ListByPrefix(ctx, bucket, prefix, pageSize, pageToken)
}

// EnsureBucket creates the bucket if it does not already exist.
func (o *ObjectStore) EnsureBucket(ctx context.Context, bucket string) error {
	return o.store.EnsureBucket(ctx, bucket)
}

// PresignPut returns a presigned URL for uploading an object.
func (o *ObjectStore) PresignPut(ctx context.Context, params PresignPutParams) (string, error) {
	if o.maxExpires > 0 && params.Expires > o.maxExpires {
		params.Expires = o.maxExpires
	}
	return o.store.PresignPut(ctx, params)
}

// PresignGet returns a presigned URL for downloading an object.
func (o *ObjectStore) PresignGet(ctx context.Context, params PresignGetParams) (string, error) {
	if o.maxExpires > 0 && params.Expires > o.maxExpires {
		params.Expires = o.maxExpires
	}
	return o.store.PresignGet(ctx, params)
}

// FileHandler returns an http.Handler for serving presigned URL uploads/downloads.
// Mount at "/files/" on your HTTP mux.
// Returns nil if the backend is not local (S3 presigned URLs go directly to S3).
func (o *ObjectStore) FileHandler() http.Handler {
	ls, ok := o.store.(*LocalStore)
	if !ok {
		return nil
	}
	return NewFileHandler(ls, ls.TokenValidator())
}

// Store returns the underlying Store interface for advanced usage.
func (o *ObjectStore) Store() Store {
	return o.store
}

// TokenValidator returns the token validator, or nil if not configured.
func (o *ObjectStore) TokenValidator() tokenstore.TokenValidator {
	return o.tokens
}
