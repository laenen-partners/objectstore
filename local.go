package objectstore

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/laenen-partners/objectstore/tokenstore"
)

// LocalStore implements Store using the local filesystem.
// Presigned URLs use tokens issued by the configured TokenValidator.
type LocalStore struct {
	basePath string
	baseURL  string // e.g. "http://localhost:3000"
	tokens   tokenstore.TokenValidator
}

// NewLocalStore creates a LocalStore rooted at basePath.
// baseURL is the public URL prefix used for presigned URLs (e.g. "http://localhost:3000").
// tokens is the TokenValidator used for issuing and validating presigned URL tokens.
func NewLocalStore(basePath, baseURL string, tokens tokenstore.TokenValidator) (*LocalStore, error) {
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return nil, fmt.Errorf("create base path: %w", err)
	}
	return &LocalStore{
		basePath: basePath,
		baseURL:  strings.TrimRight(baseURL, "/"),
		tokens:   tokens,
	}, nil
}

// TokenValidator returns the token validator used by this store.
func (s *LocalStore) TokenValidator() tokenstore.TokenValidator { return s.tokens }

// BasePath returns the root directory.
func (s *LocalStore) BasePath() string { return s.basePath }

func (s *LocalStore) PutObject(_ context.Context, bucket, key string, r io.Reader, _ int64, _ string) error {
	path := filepath.Join(s.basePath, bucket, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (s *LocalStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	path := filepath.Join(s.basePath, bucket, key)
	return os.Open(path)
}

func (s *LocalStore) HeadObject(_ context.Context, bucket, key string) (*ObjectMeta, error) {
	path := filepath.Join(s.basePath, bucket, key)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &ObjectMeta{
		Key:          key,
		Size:         info.Size(),
		LastModified: info.ModTime(),
		ETag:         fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size()),
	}, nil
}

func (s *LocalStore) DeleteObject(_ context.Context, bucket, key string) error {
	path := filepath.Join(s.basePath, bucket, key)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *LocalStore) PresignPut(ctx context.Context, params PresignPutParams) (string, error) {
	tok, err := s.tokens.Issue(ctx, tokenstore.IssueRequest{
		Method:       "PUT",
		Bucket:       params.Bucket,
		Key:          params.Key,
		Expires:      params.Expires,
		MaxSize:      params.MaxSize,
		AllowedTypes: params.AllowedTypes,
		Signature:    params.Signature,
		Scope:        params.Scope,
	})
	if err != nil {
		return "", fmt.Errorf("issue token: %w", err)
	}
	u := fmt.Sprintf("%s/files/%s/%s?method=PUT&expires=%d&token=%s",
		s.baseURL, params.Bucket, params.Key, tok.ExpiresAt, tok.Token)
	return u, nil
}

func (s *LocalStore) PresignGet(ctx context.Context, bucket, key string, expires time.Duration) (string, error) {
	tok, err := s.tokens.Issue(ctx, tokenstore.IssueRequest{
		Method:  "GET",
		Bucket:  bucket,
		Key:     key,
		Expires: expires,
	})
	if err != nil {
		return "", fmt.Errorf("issue token: %w", err)
	}
	u := fmt.Sprintf("%s/files/%s/%s?method=GET&expires=%d&token=%s",
		s.baseURL, bucket, key, tok.ExpiresAt, tok.Token)
	return u, nil
}

func (s *LocalStore) ListByPrefix(_ context.Context, bucket, prefix string) ([]string, error) {
	root := filepath.Join(s.basePath, bucket)
	var keys []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(rel, prefix) {
			keys = append(keys, rel)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return keys, err
}

func (s *LocalStore) EnsureBucket(_ context.Context, bucket string) error {
	return os.MkdirAll(filepath.Join(s.basePath, bucket), 0o755)
}

// ParseFilePath extracts bucket and key from a URL path like /files/{bucket}/{key...}.
func ParseFilePath(urlPath string) (bucket, key string, ok bool) {
	p := strings.TrimPrefix(urlPath, "/files/")
	if p == urlPath {
		return "", "", false
	}
	parts := strings.SplitN(p, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	bucket = filepath.Clean(parts[0])
	key = filepath.Clean(parts[1])
	if strings.Contains(bucket, "..") || strings.Contains(key, "..") {
		return "", "", false
	}
	return bucket, key, true
}

// QueryParam is a helper to read a query parameter from a URL.
func QueryParam(u *url.URL, name string) string {
	return u.Query().Get(name)
}
