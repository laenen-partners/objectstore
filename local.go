package objectstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/laenen-partners/objectstore/tokenstore"
)

const metaSuffix = ".objectstore-meta"

// objectMetaFile is the JSON structure stored alongside each object.
type objectMetaFile struct {
	ContentType string `json:"content_type,omitempty"`
}

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

func (s *LocalStore) PutObject(_ context.Context, bucket, key string, r io.Reader, _ int64, contentType string) error {
	path, err := safePath(s.basePath, bucket, key)
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}

	// Write content type sidecar file.
	if contentType != "" {
		meta := objectMetaFile{ContentType: contentType}
		data, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("marshal meta: %w", err)
		}
		if err := os.WriteFile(path+metaSuffix, data, 0o644); err != nil {
			return fmt.Errorf("write meta: %w", err)
		}
	}

	return nil
}

func (s *LocalStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	path, err := safePath(s.basePath, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	return os.Open(path)
}

func (s *LocalStore) HeadObject(_ context.Context, bucket, key string) (*ObjectMeta, error) {
	path, err := safePath(s.basePath, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("head object: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	meta := &ObjectMeta{
		Key:          key,
		Size:         info.Size(),
		LastModified: info.ModTime(),
		ETag:         fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size()),
	}

	// Read content type from sidecar meta file.
	if data, err := os.ReadFile(path + metaSuffix); err == nil {
		var mf objectMetaFile
		if json.Unmarshal(data, &mf) == nil {
			meta.ContentType = mf.ContentType
		}
	}

	return meta, nil
}

func (s *LocalStore) DeleteObject(_ context.Context, bucket, key string) error {
	path, err := safePath(s.basePath, bucket, key)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	// Remove sidecar meta file (ignore errors).
	os.Remove(path + metaSuffix)

	err = os.Remove(path)
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
		Tags:         params.Tags,
	})
	if err != nil {
		return "", fmt.Errorf("issue token: %w", err)
	}
	u := fmt.Sprintf("%s/files/%s/%s?method=PUT&expires=%d&token=%s",
		s.baseURL, params.Bucket, params.Key, tok.ExpiresAt, tok.Token)
	return u, nil
}

func (s *LocalStore) PresignGet(ctx context.Context, params PresignGetParams) (string, error) {
	tok, err := s.tokens.Issue(ctx, tokenstore.IssueRequest{
		Method:  "GET",
		Bucket:  params.Bucket,
		Key:     params.Key,
		Expires: params.Expires,
	})
	if err != nil {
		return "", fmt.Errorf("issue token: %w", err)
	}
	u := fmt.Sprintf("%s/files/%s/%s?method=GET&expires=%d&token=%s",
		s.baseURL, params.Bucket, params.Key, tok.ExpiresAt, tok.Token)
	if params.Filename != "" {
		u += "&filename=" + url.QueryEscape(params.Filename)
	}
	return u, nil
}

func (s *LocalStore) ListByPrefix(_ context.Context, bucket, prefix string, pageSize int, pageToken string) (*ListResult, error) {
	root, err := safePath(s.basePath, bucket)
	if err != nil {
		return nil, fmt.Errorf("list by prefix: %w", err)
	}
	var allKeys []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		// Filter out sidecar meta files.
		if strings.HasSuffix(rel, metaSuffix) {
			return nil
		}
		if strings.HasPrefix(rel, prefix) {
			allKeys = append(allKeys, rel)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return &ListResult{}, nil
	}
	if err != nil {
		return nil, err
	}

	sort.Strings(allKeys)

	// Apply page token: skip keys <= pageToken.
	if pageToken != "" {
		idx := sort.SearchStrings(allKeys, pageToken)
		if idx < len(allKeys) && allKeys[idx] == pageToken {
			idx++
		}
		allKeys = allKeys[idx:]
	}

	result := &ListResult{}
	if pageSize > 0 && len(allKeys) > pageSize {
		result.Keys = allKeys[:pageSize]
		result.NextPageToken = result.Keys[len(result.Keys)-1]
	} else {
		result.Keys = allKeys
	}

	return result, nil
}

func (s *LocalStore) EnsureBucket(_ context.Context, bucket string) error {
	path, err := safePath(s.basePath, bucket)
	if err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}
	return os.MkdirAll(path, 0o755)
}

// IsLocal returns true if the Store is a *LocalStore.
func IsLocal(s Store) bool {
	_, ok := s.(*LocalStore)
	return ok
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
