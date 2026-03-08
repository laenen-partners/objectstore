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
	"testing"
	"time"

	"connectrpc.com/connect"

	objectstorev1 "github.com/laenen-partners/objectstore/gen/objectstore/v1"
	"github.com/laenen-partners/objectstore/gen/objectstore/v1/objectstorev1connect"

	objectstore "github.com/laenen-partners/objectstore"
	"github.com/laenen-partners/objectstore/tokenstore"
)

// testTokenValidator is a simple HMAC-based token validator for tests.
type testTokenValidator struct {
	secret []byte
}

func newTestTokenValidator(secret string) *testTokenValidator {
	return &testTokenValidator{secret: []byte(secret)}
}

func (v *testTokenValidator) Issue(_ context.Context, req tokenstore.IssueRequest) (*tokenstore.Token, error) {
	expiresAt := time.Now().Add(req.Expires).Unix()
	data := fmt.Sprintf("%s:%s:%s:%d", req.Method, req.Bucket, req.Key, expiresAt)
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(data))
	token := hex.EncodeToString(mac.Sum(nil))
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
	return &tokenstore.Claims{}, nil
}

func (v *testTokenValidator) Revoke(_ context.Context, _ string) error {
	return nil
}

// startServer creates a LocalStore-backed server and returns the test server,
// the connect client, and a cleanup function.
func startServer(t *testing.T) (*httptest.Server, objectstorev1connect.ObjectStoreServiceClient) {
	t.Helper()

	dir := t.TempDir()
	cfg := objectstore.Config{
		Backend:        "file",
		BasePath:       dir,
		BaseURL:        "PLACEHOLDER", // replaced after server starts
		TokenValidator: newTestTokenValidator("test-secret-key"),
	}

	// We need to create the handler after we know the server URL, but
	// httptest.NewUnstartedServer needs the handler upfront. Use a mux swap.
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)

	// Now rebuild with correct BaseURL.
	cfg.BaseURL = ts.URL
	handler, _, err := objectstore.New(cfg)
	if err != nil {
		ts.Close()
		t.Fatalf("objectstore.New: %v", err)
	}

	// Swap the handler into the running server.
	// httptest.Server wraps our handler, so we redirect at the mux level.
	mux.Handle("/", handler)

	t.Cleanup(ts.Close)

	client := objectstorev1connect.NewObjectStoreServiceClient(ts.Client(), ts.URL)
	return ts, client
}

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
