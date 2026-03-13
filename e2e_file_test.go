package objectstore_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	objectstore "github.com/laenen-partners/objectstore"
	"github.com/laenen-partners/objectstore/tokenstore"
)

// testTokenValidator is a simple HMAC-based token validator for tests.
type testTokenValidator struct {
	secret []byte

	mu        sync.Mutex
	claims    map[string]*tokenstore.Claims
	lastIssue tokenstore.IssueRequest
	hasIssue  bool
}

func newTestTokenValidator(secret string) *testTokenValidator {
	return &testTokenValidator{
		secret: []byte(secret),
		claims: make(map[string]*tokenstore.Claims),
	}
}

func (v *testTokenValidator) Issue(_ context.Context, req tokenstore.IssueRequest) (*tokenstore.Token, error) {
	expiresAt := time.Now().Add(req.Expires).Unix()
	data := fmt.Sprintf("%s:%s:%s:%d", req.Method, req.Bucket, req.Key, expiresAt)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(data))
	token := hex.EncodeToString(mac.Sum(nil))

	v.mu.Lock()
	v.lastIssue = req
	v.hasIssue = true
	if req.MaxSize > 0 || len(req.AllowedTypes) > 0 {
		v.claims[token] = &tokenstore.Claims{
			MaxSize:      req.MaxSize,
			AllowedTypes: req.AllowedTypes,
		}
	}
	v.mu.Unlock()

	return &tokenstore.Token{Token: token, ExpiresAt: expiresAt}, nil
}

func (v *testTokenValidator) Validate(_ context.Context, method, bucket, key string, expiresAt int64, token string) (*tokenstore.Claims, error) {
	if time.Now().Unix() > expiresAt {
		return nil, tokenstore.ErrTokenExpired
	}
	data := fmt.Sprintf("%s:%s:%s:%d", method, bucket, key, expiresAt)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(data))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return nil, tokenstore.ErrTokenInvalid
	}

	v.mu.Lock()
	c, ok := v.claims[token]
	v.mu.Unlock()
	if ok {
		return c, nil
	}
	return &tokenstore.Claims{}, nil
}

func (v *testTokenValidator) Revoke(_ context.Context, _ string) error {
	return nil
}

// LastIssueRequest returns the most recent IssueRequest passed to Issue.
func (v *testTokenValidator) LastIssueRequest() (tokenstore.IssueRequest, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lastIssue, v.hasIssue
}

// startLocalServer creates a test server with a LocalStore and file handler.
// Returns: test server, store, token validator.
func startLocalServer(t *testing.T) (*httptest.Server, objectstore.Store, *testTokenValidator) {
	t.Helper()

	dir := t.TempDir()
	tv := newTestTokenValidator("test-secret-key")

	// Create a placeholder store, then re-create with correct URL after server starts.
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)

	ls, err := objectstore.NewLocalStore(dir, ts.URL, tv)
	if err != nil {
		ts.Close()
		t.Fatalf("NewLocalStore: %v", err)
	}

	mux.Handle("/files/", objectstore.NewFileHandler(ls, tv))
	t.Cleanup(ts.Close)

	return ts, ls, tv
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEnsureBucket(t *testing.T) {
	_, store, _ := startLocalServer(t)
	ctx := context.Background()

	if err := store.EnsureBucket(ctx, "my-bucket"); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// Calling again should be idempotent.
	if err := store.EnsureBucket(ctx, "my-bucket"); err != nil {
		t.Fatalf("EnsureBucket (idempotent): %v", err)
	}
}

func TestUploadDownloadViaPresignedURLs(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "test-bucket"
	const key = "docs/hello.txt"
	const body = "hello, objectstore!"

	if err := store.EnsureBucket(ctx, bucket); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// Get a presigned PUT URL.
	putURL, err := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket:      bucket,
		Key:         key,
		ContentType: "text/plain",
		Expires:     15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if putURL == "" {
		t.Fatal("PresignPut returned empty URL")
	}

	// Upload via HTTP PUT.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}

	// Get a presigned GET URL.
	getURL, err := store.PresignGet(ctx, objectstore.PresignGetParams{
		Bucket:  bucket,
		Key:     key,
		Expires: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	// Download via HTTP GET.
	getResp, err := ts.Client().Get(getURL)
	if err != nil {
		t.Fatalf("GET request: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getResp.StatusCode)
	}

	got, _ := io.ReadAll(getResp.Body)
	if string(got) != body {
		t.Fatalf("GET body = %q, want %q", got, body)
	}
}

func TestHeadObject(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "head-bucket"
	const key = "file.bin"
	content := []byte("some binary content")

	_ = store.EnsureBucket(ctx, bucket)

	putURL, _ := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket: bucket, Key: key, ContentType: "application/octet-stream", Expires: 15 * time.Minute,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(content))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	meta, err := store.HeadObject(ctx, bucket, key)
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if meta.Key != key {
		t.Errorf("HeadObject key = %q, want %q", meta.Key, key)
	}
	if meta.Size != int64(len(content)) {
		t.Errorf("HeadObject size = %d, want %d", meta.Size, len(content))
	}
}

func TestDeleteObject(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "del-bucket"
	const key = "to-delete.txt"

	_ = store.EnsureBucket(ctx, bucket)

	putURL, _ := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket: bucket, Key: key, Expires: 15 * time.Minute,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte("delete me")))
	resp, _ := ts.Client().Do(req)
	resp.Body.Close()

	if err := store.DeleteObject(ctx, bucket, key); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	// HeadObject should now fail.
	_, err := store.HeadObject(ctx, bucket, key)
	if err == nil {
		t.Fatal("HeadObject after delete should fail, but got nil error")
	}
}

func TestListByPrefix(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "list-bucket"
	_ = store.EnsureBucket(ctx, bucket)

	files := map[string]string{
		"images/a.png": "img-a",
		"images/b.png": "img-b",
		"docs/c.txt":   "doc-c",
	}
	for k, v := range files {
		putURL, _ := store.PresignPut(ctx, objectstore.PresignPutParams{
			Bucket: bucket, Key: k, Expires: 15 * time.Minute,
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte(v)))
		resp, _ := ts.Client().Do(req)
		resp.Body.Close()
	}

	result, err := store.ListByPrefix(ctx, bucket, "images/", 0, "")
	if err != nil {
		t.Fatalf("ListByPrefix: %v", err)
	}
	if len(result.Keys) != 2 {
		t.Fatalf("ListByPrefix got %d keys, want 2: %v", len(result.Keys), result.Keys)
	}

	result, err = store.ListByPrefix(ctx, bucket, "docs/", 0, "")
	if err != nil {
		t.Fatalf("ListByPrefix docs: %v", err)
	}
	if len(result.Keys) != 1 {
		t.Fatalf("ListByPrefix docs got %d keys, want 1: %v", len(result.Keys), result.Keys)
	}
}

func TestPresignedURL_InvalidToken(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "secure-bucket"
	const key = "secret.txt"

	_ = store.EnsureBucket(ctx, bucket)

	putURL, _ := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket: bucket, Key: key, Expires: 15 * time.Minute,
	})

	// Tamper with the token.
	tamperedURL := putURL + "tampered"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, tamperedURL, bytes.NewReader([]byte("data")))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT with tampered token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered token status = %d, want 403", resp.StatusCode)
	}
}

func TestPresignedURL_MethodMismatch(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "method-bucket"
	const key = "file.txt"

	_ = store.EnsureBucket(ctx, bucket)

	getURL, _ := store.PresignGet(ctx, objectstore.PresignGetParams{
		Bucket: bucket, Key: key, Expires: 15 * time.Minute,
	})

	// Try to PUT using a GET-signed URL.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, getURL, bytes.NewReader([]byte("data")))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT with GET token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("method mismatch status = %d, want 403", resp.StatusCode)
	}
}

func TestFileHandler_InvalidPath(t *testing.T) {
	ts, _, _ := startLocalServer(t)

	resp, err := ts.Client().Get(ts.URL + "/files/")
	if err != nil {
		t.Fatalf("GET /files/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid path status = %d, want 400", resp.StatusCode)
	}
}

func TestFileHandler_MissingTokenParams(t *testing.T) {
	ts, _, _ := startLocalServer(t)

	resp, err := ts.Client().Get(ts.URL + "/files/bucket/key.txt")
	if err != nil {
		t.Fatalf("GET without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing token status = %d, want 403", resp.StatusCode)
	}
}

func TestDeleteObject_Idempotent(t *testing.T) {
	_, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "idempotent-bucket"
	_ = store.EnsureBucket(ctx, bucket)

	if err := store.DeleteObject(ctx, bucket, "does-not-exist.txt"); err != nil {
		t.Fatalf("DeleteObject non-existent: %v", err)
	}
}

func TestUpload_ContentTypeEnforced(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "ct-bucket"
	const key = "image.png"

	_ = store.EnsureBucket(ctx, bucket)

	putURL, err := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket:       bucket,
		Key:          key,
		ContentType:  "image/png",
		AllowedTypes: []string{"image/png"},
		Expires:      15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	// Upload with text/plain content type should be rejected with 415.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte("not a png")))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("content type enforcement status = %d, want 415", resp.StatusCode)
	}
}

func TestUpload_MaxSizeEnforced(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "size-bucket"
	const key = "tiny.txt"

	_ = store.EnsureBucket(ctx, bucket)

	putURL, err := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket:  bucket,
		Key:     key,
		MaxSize: 10,
		Expires: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	bigBody := bytes.Repeat([]byte("x"), 1000)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected non-200 status for oversized upload, got 200")
	}
}

func TestParseFilePath(t *testing.T) {
	tests := []struct {
		name       string
		urlPath    string
		wantBucket string
		wantKey    string
		wantOK     bool
	}{
		{name: "empty path", urlPath: "", wantOK: false},
		{name: "no files prefix", urlPath: "/other/bucket/key", wantOK: false},
		{name: "files prefix only", urlPath: "/files/", wantOK: false},
		{name: "bucket only no key", urlPath: "/files/bucket/", wantOK: false},
		{name: "bucket only no slash", urlPath: "/files/bucket", wantOK: false},
		{name: "traversal in bucket", urlPath: "/files/../etc/key.txt", wantOK: false},
		{name: "traversal in key", urlPath: "/files/bucket/../../../etc/passwd", wantOK: false},
		{name: "valid simple", urlPath: "/files/mybucket/mykey.txt", wantBucket: "mybucket", wantKey: "mykey.txt", wantOK: true},
		{name: "valid nested key", urlPath: "/files/mybucket/path/to/file.txt", wantBucket: "mybucket", wantKey: "path/to/file.txt", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, key, ok := objectstore.ParseFilePath(tt.urlPath)
			if ok != tt.wantOK {
				t.Fatalf("ParseFilePath(%q) ok = %v, want %v", tt.urlPath, ok, tt.wantOK)
			}
			if ok {
				if bucket != tt.wantBucket {
					t.Errorf("bucket = %q, want %q", bucket, tt.wantBucket)
				}
				if key != tt.wantKey {
					t.Errorf("key = %q, want %q", key, tt.wantKey)
				}
			}
		})
	}
}

func TestHeadHTTPRequest(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "head-http-bucket"
	const key = "headable.txt"
	const body = "head me"

	_ = store.EnsureBucket(ctx, bucket)

	putURL, _ := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket: bucket, Key: key, ContentType: "text/plain", Expires: 15 * time.Minute,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	getURL, _ := store.PresignGet(ctx, objectstore.PresignGetParams{
		Bucket: bucket, Key: key, Expires: 15 * time.Minute,
	})

	headReq, _ := http.NewRequestWithContext(ctx, http.MethodHead, getURL, nil)
	headResp, err := ts.Client().Do(headReq)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer headResp.Body.Close()

	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", headResp.StatusCode)
	}

	headBody, _ := io.ReadAll(headResp.Body)
	if len(headBody) != 0 {
		t.Errorf("HEAD body length = %d, want 0", len(headBody))
	}
}

func TestIsLocal(t *testing.T) {
	dir := t.TempDir()
	tv := newTestTokenValidator("test")

	ls, err := objectstore.NewLocalStore(dir, "http://localhost:3000", tv)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}

	if !objectstore.IsLocal(ls) {
		t.Error("IsLocal(LocalStore) = false, want true")
	}

	if objectstore.IsLocal(nil) {
		t.Error("IsLocal(nil) = true, want false")
	}
}

func TestFileHandler_MethodNotAllowed(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "method-na-bucket"
	const key = "file.txt"

	_ = store.EnsureBucket(ctx, bucket)

	getURL, _ := store.PresignGet(ctx, objectstore.PresignGetParams{
		Bucket: bucket, Key: key, Expires: 15 * time.Minute,
	})

	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, getURL, nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", resp.StatusCode)
	}
}

func TestUpload_ContentTypeAllowed(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "ct-ok-bucket"
	const key = "image.png"

	_ = store.EnsureBucket(ctx, bucket)

	putURL, err := store.PresignPut(ctx, objectstore.PresignPutParams{
		Bucket:       bucket,
		Key:          key,
		ContentType:  "image/png",
		AllowedTypes: []string{"image/png"},
		Expires:      15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte("png data")))
	req.Header.Set("Content-Type", "image/png")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
}

func TestServeGet_NotFound(t *testing.T) {
	ts, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "notfound-bucket"
	const key = "nonexistent.txt"

	_ = store.EnsureBucket(ctx, bucket)

	getURL, _ := store.PresignGet(ctx, objectstore.PresignGetParams{
		Bucket: bucket, Key: key, Expires: 15 * time.Minute,
	})

	resp, err := ts.Client().Get(getURL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET nonexistent status = %d, want 404", resp.StatusCode)
	}
}

func TestSafePath_EmptyComponent(t *testing.T) {
	_, store, _ := startLocalServer(t)
	ctx := context.Background()

	err := store.EnsureBucket(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty bucket, got nil")
	}
}

func TestPresignGet_ContainsExpires(t *testing.T) {
	_, store, _ := startLocalServer(t)
	ctx := context.Background()

	const bucket = "get-expires-bucket"
	const key = "file.txt"

	_ = store.EnsureBucket(ctx, bucket)

	getURL, err := store.PresignGet(ctx, objectstore.PresignGetParams{
		Bucket:  bucket,
		Key:     key,
		Expires: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if getURL == "" {
		t.Fatal("PresignGet returned empty URL")
	}
	if !strings.Contains(getURL, "expires=") {
		t.Fatal("URL missing expires parameter")
	}
}
