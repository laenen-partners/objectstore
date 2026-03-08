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

	"connectrpc.com/connect"

	objectstorev1 "github.com/laenen-partners/objectstore/gen/objectstore/v1"
	"github.com/laenen-partners/objectstore/gen/objectstore/v1/objectstorev1connect"

	objectstore "github.com/laenen-partners/objectstore"
	"github.com/laenen-partners/objectstore/tokenstore"
)

// testTokenValidator is a simple HMAC-based token validator for tests.
// It supports storing claims (MaxSize, AllowedTypes) keyed by token
// and recording the last IssueRequest for inspection.
type testTokenValidator struct {
	secret []byte

	mu         sync.Mutex
	claims     map[string]*tokenstore.Claims
	lastIssue  tokenstore.IssueRequest
	hasIssue   bool
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
	// Store claims if MaxSize or AllowedTypes are set.
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

// startServerWithConfig creates a test server with a customizable Config.
// The provided fn callback can modify the config before the server is created.
func startServerWithConfig(t *testing.T, fn func(*objectstore.Config)) (*httptest.Server, *testTokenValidator) {
	t.Helper()

	dir := t.TempDir()
	tv := newTestTokenValidator("test-secret-key")
	cfg := objectstore.Config{
		Backend:        "file",
		BasePath:       dir,
		BaseURL:        "PLACEHOLDER",
		TokenValidator: tv,
	}

	if fn != nil {
		fn(&cfg)
	}

	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)

	cfg.BaseURL = ts.URL
	handler, _, err := objectstore.New(cfg)
	if err != nil {
		ts.Close()
		t.Fatalf("objectstore.New: %v", err)
	}

	mux.Handle("/", handler)
	t.Cleanup(ts.Close)

	return ts, tv
}

// startServer creates a LocalStore-backed server and returns the test server,
// the connect client, and a cleanup function.
func startServer(t *testing.T) (*httptest.Server, objectstorev1connect.ObjectStoreServiceClient) {
	t.Helper()

	ts, _ := startServerWithConfig(t, nil)
	client := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)
	return ts, client
}

// withBearerAuth returns a Connect unary interceptor that sets the Authorization header.
func withBearerAuth(key string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+key)
			return next(ctx, req)
		}
	}
}

// withHeaders returns a Connect unary interceptor that sets arbitrary headers.
func withHeaders(headers map[string]string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			for k, v := range headers {
				req.Header().Set(k, v)
			}
			return next(ctx, req)
		}
	}
}

// ---------------------------------------------------------------------------
// Existing tests
// ---------------------------------------------------------------------------

func TestE2E_EnsureBucket(t *testing.T) {
	_, client := startServer(t)
	ctx := context.Background()

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: "my-bucket",
	}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// Calling again should be idempotent.
	_, err = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: "my-bucket",
	}))
	if err != nil {
		t.Fatalf("EnsureBucket (idempotent): %v", err)
	}
}

func TestE2E_UploadDownloadViaPresignedURLs(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "test-bucket"
	const key = "docs/hello.txt"
	const body = "hello, objectstore!"

	// 1. Ensure bucket exists.
	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: bucket,
	}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// 2. Get a presigned PUT URL.
	putResp, err := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket:      bucket,
		Key:         key,
		ContentType: "text/plain",
	}))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	putURL := putResp.Msg.Url
	if putURL == "" {
		t.Fatal("PresignPut returned empty URL")
	}

	// 3. Upload the file via HTTP PUT to the presigned URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("create PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}

	// 4. Get a presigned GET URL.
	getResp, err := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket: bucket,
		Key:    key,
	}))
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	getURL := getResp.Msg.Url
	if getURL == "" {
		t.Fatal("PresignGet returned empty URL")
	}

	// 5. Download the file via HTTP GET.
	getHTTPResp, err := ts.Client().Get(getURL)
	if err != nil {
		t.Fatalf("GET request: %v", err)
	}
	defer getHTTPResp.Body.Close()
	if getHTTPResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getHTTPResp.StatusCode)
	}

	got, err := io.ReadAll(getHTTPResp.Body)
	if err != nil {
		t.Fatalf("read GET body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("GET body = %q, want %q", got, body)
	}
}

func TestE2E_HeadObject(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "head-bucket"
	const key = "file.bin"
	content := []byte("some binary content")

	// Setup: create bucket and upload a file.
	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	putResp, _ := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket: bucket, Key: key, ContentType: "application/octet-stream",
	}))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader(content))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	// HeadObject via RPC.
	headResp, err := client.HeadObject(ctx, connect.NewRequest(&objectstorev1.HeadObjectRequest{
		Bucket: bucket, Key: key,
	}))
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if headResp.Msg.Key != key {
		t.Errorf("HeadObject key = %q, want %q", headResp.Msg.Key, key)
	}
	if headResp.Msg.Size != int64(len(content)) {
		t.Errorf("HeadObject size = %d, want %d", headResp.Msg.Size, len(content))
	}
}

func TestE2E_DeleteObject(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "del-bucket"
	const key = "to-delete.txt"

	// Setup: create and upload.
	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	putResp, _ := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket: bucket, Key: key,
	}))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader([]byte("delete me")))
	resp, _ := ts.Client().Do(req)
	resp.Body.Close()

	// Delete via RPC.
	_, err := client.DeleteObject(ctx, connect.NewRequest(&objectstorev1.DeleteObjectRequest{
		Bucket: bucket, Key: key,
	}))
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	// HeadObject should now fail.
	_, err = client.HeadObject(ctx, connect.NewRequest(&objectstorev1.HeadObjectRequest{
		Bucket: bucket, Key: key,
	}))
	if err == nil {
		t.Fatal("HeadObject after delete should fail, but got nil error")
	}
}

func TestE2E_ListByPrefix(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "list-bucket"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// Upload several files under different prefixes.
	files := map[string]string{
		"images/a.png": "img-a",
		"images/b.png": "img-b",
		"docs/c.txt":   "doc-c",
	}
	for k, v := range files {
		putResp, _ := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
			Bucket: bucket, Key: k,
		}))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader([]byte(v)))
		resp, _ := ts.Client().Do(req)
		resp.Body.Close()
	}

	// List by prefix "images/".
	listResp, err := client.ListByPrefix(ctx, connect.NewRequest(&objectstorev1.ListByPrefixRequest{
		Bucket: bucket, Prefix: "images/",
	}))
	if err != nil {
		t.Fatalf("ListByPrefix: %v", err)
	}
	if len(listResp.Msg.Keys) != 2 {
		t.Fatalf("ListByPrefix got %d keys, want 2: %v", len(listResp.Msg.Keys), listResp.Msg.Keys)
	}

	// List by prefix "docs/".
	listResp, err = client.ListByPrefix(ctx, connect.NewRequest(&objectstorev1.ListByPrefixRequest{
		Bucket: bucket, Prefix: "docs/",
	}))
	if err != nil {
		t.Fatalf("ListByPrefix docs: %v", err)
	}
	if len(listResp.Msg.Keys) != 1 {
		t.Fatalf("ListByPrefix docs got %d keys, want 1: %v", len(listResp.Msg.Keys), listResp.Msg.Keys)
	}
}

func TestE2E_PresignedURL_InvalidToken(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "secure-bucket"
	const key = "secret.txt"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// Get a valid presigned PUT URL.
	putResp, _ := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket: bucket, Key: key,
	}))

	// Tamper with the token.
	tamperedURL := putResp.Msg.Url + "tampered"
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

func TestE2E_PresignedURL_MethodMismatch(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "method-bucket"
	const key = "file.txt"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// Get a presigned GET URL.
	getResp, _ := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket: bucket, Key: key,
	}))

	// Try to PUT using a GET-signed URL.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, getResp.Msg.Url, bytes.NewReader([]byte("data")))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT with GET token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("method mismatch status = %d, want 403", resp.StatusCode)
	}
}

func TestE2E_FileHandler_InvalidPath(t *testing.T) {
	ts, _ := startServer(t)

	// Request to /files/ without bucket/key.
	resp, err := ts.Client().Get(ts.URL + "/files/")
	if err != nil {
		t.Fatalf("GET /files/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid path status = %d, want 400", resp.StatusCode)
	}
}

func TestE2E_FileHandler_MissingTokenParams(t *testing.T) {
	ts, _ := startServer(t)

	// Request with valid path but no token query params.
	resp, err := ts.Client().Get(ts.URL + "/files/bucket/key.txt")
	if err != nil {
		t.Fatalf("GET without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing token status = %d, want 403", resp.StatusCode)
	}
}

func TestE2E_DeleteObject_Idempotent(t *testing.T) {
	_, client := startServer(t)
	ctx := context.Background()

	const bucket = "idempotent-bucket"
	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// Deleting a non-existent object should not error.
	_, err := client.DeleteObject(ctx, connect.NewRequest(&objectstorev1.DeleteObjectRequest{
		Bucket: bucket, Key: "does-not-exist.txt",
	}))
	if err != nil {
		t.Fatalf("DeleteObject non-existent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// New tests
// ---------------------------------------------------------------------------

func TestE2E_Auth_RejectsUnauthenticated(t *testing.T) {
	ts, _ := startServerWithConfig(t, func(cfg *objectstore.Config) {
		cfg.APIKeys = []string{"valid-key-123"}
	})

	// Client without auth header should get CodeUnauthenticated.
	client := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)
	_, err := client.EnsureBucket(context.Background(), connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: "test",
	}))
	if err == nil {
		t.Fatal("expected error for unauthenticated request, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", code)
	}
}

func TestE2E_Auth_RejectsInvalidKey(t *testing.T) {
	ts, _ := startServerWithConfig(t, func(cfg *objectstore.Config) {
		cfg.APIKeys = []string{"valid-key-123"}
	})

	// Client with wrong Bearer key should get CodeUnauthenticated.
	client := objectstorev1connect.NewObjectStoreServiceClient(
		ts.Client(), ts.URL,
		connect.WithInterceptors(withBearerAuth("wrong-key")),
	)
	_, err := client.EnsureBucket(context.Background(), connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: "test",
	}))
	if err == nil {
		t.Fatal("expected error for invalid key, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", code)
	}
}

func TestE2E_Auth_AcceptsValidKey(t *testing.T) {
	ts, _ := startServerWithConfig(t, func(cfg *objectstore.Config) {
		cfg.APIKeys = []string{"valid-key-123"}
	})

	client := objectstorev1connect.NewObjectStoreServiceClient(
		ts.Client(), ts.URL,
		connect.WithInterceptors(withBearerAuth("valid-key-123")),
	)
	_, err := client.EnsureBucket(context.Background(), connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: "auth-bucket",
	}))
	if err != nil {
		t.Fatalf("EnsureBucket with valid key: %v", err)
	}
}

func TestE2E_Auth_FileHandlerNoAuthRequired(t *testing.T) {
	ts, _ := startServerWithConfig(t, func(cfg *objectstore.Config) {
		cfg.APIKeys = []string{"valid-key-123"}
	})
	ctx := context.Background()

	// Use authenticated client for RPC calls.
	client := objectstorev1connect.NewObjectStoreServiceClient(
		ts.Client(), ts.URL,
		connect.WithInterceptors(withBearerAuth("valid-key-123")),
	)

	const bucket = "auth-file-bucket"
	const key = "test.txt"
	const body = "file handler no auth"

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	putResp, err := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket: bucket, Key: key, ContentType: "text/plain",
	}))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	// Upload via presigned URL without any Authorization header.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}

	// Download via presigned URL without any Authorization header.
	getResp, err := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket: bucket, Key: key,
	}))
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	httpResp, err := ts.Client().Get(getResp.Msg.Url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", httpResp.StatusCode)
	}
	got, _ := io.ReadAll(httpResp.Body)
	if string(got) != body {
		t.Fatalf("GET body = %q, want %q", got, body)
	}
}

func TestE2E_Auth_CallerIdentityInTags(t *testing.T) {
	ts, tv := startServerWithConfig(t, func(cfg *objectstore.Config) {
		cfg.APIKeys = []string{"valid-key-123"}
	})
	ctx := context.Background()

	client := objectstorev1connect.NewObjectStoreServiceClient(
		ts.Client(), ts.URL,
		connect.WithInterceptors(
			withBearerAuth("valid-key-123"),
			withHeaders(map[string]string{
				"X-User-ID":    "user-42",
				"X-Service-ID": "svc-web",
			}),
		),
	)

	const bucket = "tags-bucket"
	const key = "tagged.txt"

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	_, err = client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket: bucket, Key: key, ContentType: "text/plain",
	}))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	// Inspect the IssueRequest that was passed to the token validator.
	issueReq, ok := tv.LastIssueRequest()
	if !ok {
		t.Fatal("expected IssueRequest to have been recorded")
	}
	if issueReq.Tags == nil {
		t.Fatal("expected Tags to be non-nil")
	}
	if got := issueReq.Tags["_user_id"]; got != "user-42" {
		t.Errorf("_user_id tag = %q, want %q", got, "user-42")
	}
	if got := issueReq.Tags["_service_id"]; got != "svc-web" {
		t.Errorf("_service_id tag = %q, want %q", got, "svc-web")
	}
}

func TestE2E_SecurityHeaders(t *testing.T) {
	ts, _ := startServerWithConfig(t, nil)

	// Any request should include security headers.
	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":       "DENY",
		"X-Xss-Protection":      "0",
		"Referrer-Policy":       "strict-origin-when-cross-origin",
	}
	for header, want := range checks {
		got := resp.Header.Get(header)
		if got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
}

func TestE2E_HealthEndpoints(t *testing.T) {
	ts, _ := startServerWithConfig(t, nil)

	for _, endpoint := range []string{"/healthz", "/readyz"} {
		resp, err := ts.Client().Get(ts.URL + endpoint)
		if err != nil {
			t.Fatalf("GET %s: %v", endpoint, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", endpoint, resp.StatusCode)
		}
		if string(body) != "ok" {
			t.Errorf("%s body = %q, want %q", endpoint, body, "ok")
		}
	}
}

func TestE2E_RateLimit(t *testing.T) {
	ts, _ := startServerWithConfig(t, func(cfg *objectstore.Config) {
		cfg.RateLimit = 1
		cfg.RateBurst = 1
	})

	// First request should succeed (consumes the single burst token).
	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp.StatusCode)
	}

	// Rapid follow-up requests should be rate limited.
	// Send several quickly to ensure at least one gets 429.
	got429 := false
	for i := 0; i < 10; i++ {
		resp, err = ts.Client().Get(ts.URL + "/healthz")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected at least one 429 response from rate limiter")
	}
}

func TestE2E_Upload_ContentTypeEnforced(t *testing.T) {
	ts, _ := startServerWithConfig(t, nil)
	ctx := context.Background()
	client := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)

	const bucket = "ct-bucket"
	const key = "image.png"

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// Issue a presigned URL that only allows image/png.
	putResp, err := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket:       bucket,
		Key:          key,
		ContentType:  "image/png",
		AllowedTypes: []string{"image/png"},
	}))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	// Upload with text/plain content type should be rejected with 415.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader([]byte("not a png")))
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

func TestE2E_Upload_MaxSizeEnforced(t *testing.T) {
	ts, _ := startServerWithConfig(t, nil)
	ctx := context.Background()
	client := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)

	const bucket = "size-bucket"
	const key = "tiny.txt"

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// Issue a presigned URL with MaxSize=10 bytes.
	putResp, err := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket:  bucket,
		Key:     key,
		MaxSize: 10,
	}))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	// Upload 1000 bytes, exceeding the limit.
	bigBody := bytes.Repeat([]byte("x"), 1000)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	// MaxBytesReader causes a 500 (upload failed) when body exceeds the limit.
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected non-200 status for oversized upload, got 200")
	}
}

func TestE2E_PresignPut_CustomExpires(t *testing.T) {
	_, client := startServer(t)
	ctx := context.Background()

	const bucket = "expires-bucket"
	const key = "expires.txt"

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// PresignPut with custom ExpiresSeconds=60.
	putResp, err := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket:         bucket,
		Key:            key,
		ContentType:    "text/plain",
		ExpiresSeconds: 60,
	}))
	if err != nil {
		t.Fatalf("PresignPut with ExpiresSeconds=60: %v", err)
	}
	if putResp.Msg.Url == "" {
		t.Fatal("PresignPut returned empty URL")
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
		{
			name:    "empty path",
			urlPath: "",
			wantOK:  false,
		},
		{
			name:    "no files prefix",
			urlPath: "/other/bucket/key",
			wantOK:  false,
		},
		{
			name:    "files prefix only",
			urlPath: "/files/",
			wantOK:  false,
		},
		{
			name:    "bucket only no key",
			urlPath: "/files/bucket/",
			wantOK:  false,
		},
		{
			name:    "bucket only no slash",
			urlPath: "/files/bucket",
			wantOK:  false,
		},
		{
			name:    "traversal in bucket",
			urlPath: "/files/../etc/key.txt",
			wantOK:  false,
		},
		{
			name:    "traversal in key",
			urlPath: "/files/bucket/../../../etc/passwd",
			wantOK:  false,
		},
		{
			name:       "valid simple",
			urlPath:    "/files/mybucket/mykey.txt",
			wantBucket: "mybucket",
			wantKey:    "mykey.txt",
			wantOK:     true,
		},
		{
			name:       "valid nested key",
			urlPath:    "/files/mybucket/path/to/file.txt",
			wantBucket: "mybucket",
			wantKey:    "path/to/file.txt",
			wantOK:     true,
		},
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

func TestE2E_HeadHTTPRequest(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "head-http-bucket"
	const key = "headable.txt"
	const body = "head me"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	putResp, _ := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket: bucket, Key: key, ContentType: "text/plain",
	}))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	// Get a presigned GET URL.
	getResp, _ := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket: bucket, Key: key,
	}))

	// Send HTTP HEAD request to presigned GET URL.
	headReq, _ := http.NewRequestWithContext(ctx, http.MethodHead, getResp.Msg.Url, nil)
	headResp, err := ts.Client().Do(headReq)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer headResp.Body.Close()

	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", headResp.StatusCode)
	}

	// Body should be empty for HEAD request.
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

	// nil should return false (not panic).
	if objectstore.IsLocal(nil) {
		t.Error("IsLocal(nil) = true, want false")
	}
}

func TestE2E_ConfigFromEnv(t *testing.T) {
	// t.Setenv automatically restores the original value after the test.
	envVars := []string{
		"OBJECT_STORE", "OBJECT_STORE_PATH", "OBJECT_STORE_URL",
		"OBJECT_STORE_POSTGRES_URL", "S3_REGION", "S3_ENDPOINT",
		"API_KEYS", "RATE_LIMIT", "RATE_BURST",
	}
	for _, k := range envVars {
		t.Setenv(k, "")
	}

	cfg := objectstore.ConfigFromEnv()
	if cfg.Backend != "file" {
		t.Errorf("default Backend = %q, want %q", cfg.Backend, "file")
	}
	if cfg.BasePath != ".data/objects" {
		t.Errorf("default BasePath = %q, want %q", cfg.BasePath, ".data/objects")
	}
	if cfg.BaseURL != "http://localhost:3000" {
		t.Errorf("default BaseURL = %q, want %q", cfg.BaseURL, "http://localhost:3000")
	}

	// Test custom values.
	t.Setenv("OBJECT_STORE", "s3")
	t.Setenv("OBJECT_STORE_PATH", "/custom/path")
	t.Setenv("OBJECT_STORE_URL", "https://cdn.example.com")
	t.Setenv("API_KEYS", "key1, key2 ,key3")
	t.Setenv("RATE_LIMIT", "50.5")
	t.Setenv("RATE_BURST", "100")
	t.Setenv("S3_REGION", "us-west-2")
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("OBJECT_STORE_POSTGRES_URL", "postgres://localhost/test")

	cfg = objectstore.ConfigFromEnv()
	if cfg.Backend != "s3" {
		t.Errorf("Backend = %q, want %q", cfg.Backend, "s3")
	}
	if cfg.BasePath != "/custom/path" {
		t.Errorf("BasePath = %q, want %q", cfg.BasePath, "/custom/path")
	}
	if cfg.BaseURL != "https://cdn.example.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://cdn.example.com")
	}
	if len(cfg.APIKeys) != 3 || cfg.APIKeys[0] != "key1" || cfg.APIKeys[1] != "key2" || cfg.APIKeys[2] != "key3" {
		t.Errorf("APIKeys = %v, want [key1 key2 key3]", cfg.APIKeys)
	}
	if cfg.RateLimit != 50.5 {
		t.Errorf("RateLimit = %f, want 50.5", cfg.RateLimit)
	}
	if cfg.RateBurst != 100 {
		t.Errorf("RateBurst = %d, want 100", cfg.RateBurst)
	}
	if cfg.S3Region != "us-west-2" {
		t.Errorf("S3Region = %q, want %q", cfg.S3Region, "us-west-2")
	}
	if cfg.S3Endpoint != "http://minio:9000" {
		t.Errorf("S3Endpoint = %q, want %q", cfg.S3Endpoint, "http://minio:9000")
	}
	if cfg.PostgresURL != "postgres://localhost/test" {
		t.Errorf("PostgresURL = %q, want %q", cfg.PostgresURL, "postgres://localhost/test")
	}
}

func TestE2E_FileHandler_MethodNotAllowed(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "method-na-bucket"
	const key = "file.txt"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// Get a valid presigned GET URL.
	getResp, _ := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket: bucket, Key: key,
	}))

	// Send a DELETE request (not GET, HEAD, or PUT) -- should get 405.
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, getResp.Msg.Url, nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", resp.StatusCode)
	}
}

func TestE2E_Upload_ContentTypeAllowed(t *testing.T) {
	ts, _ := startServerWithConfig(t, nil)
	ctx := context.Background()
	client := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)

	const bucket = "ct-ok-bucket"
	const key = "image.png"

	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	// Issue a presigned URL that allows image/png.
	putResp, err := client.PresignPut(ctx, connect.NewRequest(&objectstorev1.PresignPutRequest{
		Bucket:       bucket,
		Key:          key,
		ContentType:  "image/png",
		AllowedTypes: []string{"image/png"},
	}))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	// Upload with correct content type should succeed.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putResp.Msg.Url, bytes.NewReader([]byte("png data")))
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

func TestE2E_ServeGet_NotFound(t *testing.T) {
	ts, client := startServer(t)
	ctx := context.Background()

	const bucket = "notfound-bucket"
	const key = "nonexistent.txt"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// Get a presigned GET URL for a file that doesn't exist.
	getResp, _ := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket: bucket, Key: key,
	}))

	resp, err := ts.Client().Get(getResp.Msg.Url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET nonexistent status = %d, want 404", resp.StatusCode)
	}
}

func TestE2E_SafePath_EmptyComponent(t *testing.T) {
	_, client := startServer(t)
	ctx := context.Background()

	// Bucket with empty string should fail at the RPC level.
	_, err := client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{
		Bucket: "",
	}))
	if err == nil {
		t.Fatal("expected error for empty bucket, got nil")
	}
}

func TestE2E_PresignGet_CustomExpires(t *testing.T) {
	_, client := startServer(t)
	ctx := context.Background()

	const bucket = "get-expires-bucket"
	const key = "file.txt"

	_, _ = client.EnsureBucket(ctx, connect.NewRequest(&objectstorev1.EnsureBucketRequest{Bucket: bucket}))

	// PresignGet with custom ExpiresSeconds.
	getResp, err := client.PresignGet(ctx, connect.NewRequest(&objectstorev1.PresignGetRequest{
		Bucket:         bucket,
		Key:            key,
		ExpiresSeconds: 120,
	}))
	if err != nil {
		t.Fatalf("PresignGet with ExpiresSeconds=120: %v", err)
	}
	if getResp.Msg.Url == "" {
		t.Fatal("PresignGet returned empty URL")
	}
	// Verify the URL contains the expires parameter.
	if !strings.Contains(getResp.Msg.Url, "expires=") {
		t.Fatal("URL missing expires parameter")
	}
}
