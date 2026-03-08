package objectstore

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/laenen-partners/objectstore/tokenstore"
)

// Config holds settings for creating a Store.
type Config struct {
	Backend        string                    // "file" or "s3" (default "file")
	BasePath       string                    // local: base dir (default ".data/objects")
	BaseURL        string                    // local: public URL prefix (default "http://localhost:3000")
	TokenValidator tokenstore.TokenValidator // required for file backend
	PostgresURL    string                    // Postgres connection string
	S3Region       string                    // s3: AWS region
	S3Endpoint     string                    // s3: custom endpoint (MinIO)
	APIKeys        []string                  // API keys for RPC authentication
	RateLimit      float64                   // requests per second per IP (0 = disabled)
	RateBurst      int                       // burst allowance per IP
	CORSOrigins    []string                  // allowed CORS origins (empty = no CORS)
	MaxExpires     time.Duration             // max presigned URL lifetime (0 = no cap)
}

// ConfigFromEnv reads configuration from environment variables.
//
// OBJECT_STORE selects the storage backend:
//   - "file" (default): local filesystem via LocalStore
//   - "s3": AWS S3 or S3-compatible (MinIO)
//
// LocalStore env:
//   - OBJECT_STORE_PATH: base directory (default ".data/objects")
//   - OBJECT_STORE_URL: public base URL for presigned URLs (default "http://localhost:3000")
//   - OBJECT_STORE_POSTGRES_URL: Postgres connection string (required for file backend)
//
// S3Store env:
//   - S3_REGION: AWS region
//   - S3_ENDPOINT: custom endpoint for MinIO etc.
func ConfigFromEnv() Config {
	backend := os.Getenv("OBJECT_STORE")
	if backend == "" {
		backend = "file"
	}
	basePath := os.Getenv("OBJECT_STORE_PATH")
	if basePath == "" {
		basePath = ".data/objects"
	}
	baseURL := os.Getenv("OBJECT_STORE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:3000"
	}
	var apiKeys []string
	if keys := os.Getenv("API_KEYS"); keys != "" {
		for _, k := range strings.Split(keys, ",") {
			if trimmed := strings.TrimSpace(k); trimmed != "" {
				apiKeys = append(apiKeys, trimmed)
			}
		}
	}

	rateLimit := 10.0
	if v := os.Getenv("RATE_LIMIT"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			rateLimit = parsed
		}
	}

	rateBurst := 20
	if v := os.Getenv("RATE_BURST"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			rateBurst = parsed
		}
	}

	var corsOrigins []string
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				corsOrigins = append(corsOrigins, trimmed)
			}
		}
	}

	var maxExpires time.Duration
	if v := os.Getenv("MAX_EXPIRES_SECONDS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			maxExpires = time.Duration(secs) * time.Second
		}
	}

	return Config{
		Backend:     backend,
		BasePath:    basePath,
		BaseURL:     baseURL,
		PostgresURL: os.Getenv("OBJECT_STORE_POSTGRES_URL"),
		S3Region:    os.Getenv("S3_REGION"),
		S3Endpoint:  os.Getenv("S3_ENDPOINT"),
		APIKeys:     apiKeys,
		RateLimit:   rateLimit,
		RateBurst:   rateBurst,
		CORSOrigins: corsOrigins,
		MaxExpires:  maxExpires,
	}
}

// NewStore creates a Store from the config.
func NewStore(cfg Config) (Store, error) {
	switch cfg.Backend {
	case "file", "":
		if cfg.TokenValidator == nil {
			return nil, fmt.Errorf("objectstore: TokenValidator is required for file backend")
		}
		return NewLocalStore(cfg.BasePath, cfg.BaseURL, cfg.TokenValidator)
	case "s3":
		return newS3FromConfig(cfg)
	default:
		return nil, fmt.Errorf("objectstore: unknown backend: %q", cfg.Backend)
	}
}

func newS3FromConfig(cfg Config) (*S3Store, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.S3Region),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	var opts []func(*s3.Options)
	if cfg.S3Endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = true // needed for MinIO
		})
	}

	client := s3.NewFromConfig(awsCfg, opts...)
	return NewS3Store(client), nil
}

// IsLocal returns true if the Store is a *LocalStore.
func IsLocal(s Store) bool {
	_, ok := s.(*LocalStore)
	return ok
}
