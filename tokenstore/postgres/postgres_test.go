package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	objectstore "github.com/laenen-partners/objectstore"
	"github.com/laenen-partners/objectstore/tokenstore"
	pgvalidator "github.com/laenen-partners/objectstore/tokenstore/postgres"
)

func testURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("OBJECT_STORE_POSTGRES_URL")
	if u == "" {
		t.Skip("OBJECT_STORE_POSTGRES_URL not set")
	}
	return u
}

func newValidator(t *testing.T) *pgvalidator.Validator {
	t.Helper()
	ctx := context.Background()
	v, err := pgvalidator.New(ctx, testURL(t), pgvalidator.WithMigrations())
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	t.Cleanup(v.Close)
	return v
}

func TestIssueAndValidate(t *testing.T) {
	v := newValidator(t)
	ctx := context.Background()

	tok, err := v.Issue(ctx, tokenstore.IssueRequest{
		Method:  "GET",
		Bucket:  "bucket",
		Key:     "file.txt",
		Expires: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := v.Validate(ctx, "GET", "bucket", "file.txt", tok.ExpiresAt, tok.Token); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_WrongMethod(t *testing.T) {
	v := newValidator(t)
	ctx := context.Background()

	tok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: "b", Key: "k", Expires: 5 * time.Minute,
	})

	_, err := v.Validate(ctx, "PUT", "b", "k", tok.ExpiresAt, tok.Token)
	if !errors.Is(err, tokenstore.ErrTokenInvalid) {
		t.Fatalf("got %v, want ErrTokenInvalid", err)
	}
}

func TestValidate_Expired(t *testing.T) {
	v := newValidator(t)
	ctx := context.Background()

	tok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: "b", Key: "k", Expires: -1 * time.Second,
	})

	_, err := v.Validate(ctx, "GET", "b", "k", tok.ExpiresAt, tok.Token)
	if !errors.Is(err, tokenstore.ErrTokenExpired) {
		t.Fatalf("got %v, want ErrTokenExpired", err)
	}
}

func TestRevoke(t *testing.T) {
	v := newValidator(t)
	ctx := context.Background()

	tok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: "b", Key: "k", Expires: 5 * time.Minute,
	})

	if err := v.Revoke(ctx, tok.Token); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err := v.Validate(ctx, "GET", "b", "k", tok.ExpiresAt, tok.Token)
	if !errors.Is(err, tokenstore.ErrTokenRevoked) {
		t.Fatalf("got %v, want ErrTokenRevoked", err)
	}
}

func TestOneTimeToken(t *testing.T) {
	v := newValidator(t)
	ctx := context.Background()

	tok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: "b", Key: "k", Expires: 5 * time.Minute, OneTime: true,
	})

	// First use succeeds.
	if _, err := v.Validate(ctx, "GET", "b", "k", tok.ExpiresAt, tok.Token); err != nil {
		t.Fatalf("first Validate: %v", err)
	}

	// Second use fails.
	_, err := v.Validate(ctx, "GET", "b", "k", tok.ExpiresAt, tok.Token)
	if !errors.Is(err, tokenstore.ErrTokenInvalid) {
		t.Fatalf("second Validate: got %v, want ErrTokenInvalid", err)
	}
}

func TestTags(t *testing.T) {
	v := newValidator(t)
	ctx := context.Background()

	tags := map[string]string{"user": "alice", "session": "abc123"}
	_, err := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: "b", Key: "k", Expires: 5 * time.Minute, Tags: tags,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find by tag subset.
	rows, err := v.FindByTags(ctx, map[string]string{"user": "alice"})
	if err != nil {
		t.Fatalf("FindByTags: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("FindByTags: expected at least 1 row")
	}

	// Revoke by tags.
	n, err := v.RevokeByTags(ctx, map[string]string{"session": "abc123"})
	if err != nil {
		t.Fatalf("RevokeByTags: %v", err)
	}
	if n == 0 {
		t.Fatal("RevokeByTags: expected at least 1 revoked")
	}
}

// startE2EServer creates a LocalStore backed by the Postgres TokenValidator,
// wires up the HTTP server, and returns the test server + HTTP client.
func startE2EServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	v := newValidator(t)

	dir := t.TempDir()

	// Create a mux first with a placeholder, then swap after we know the URL.
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)

	cfg := objectstore.Config{
		Backend:        "file",
		BasePath:       dir,
		BaseURL:        ts.URL,
		TokenValidator: v,
	}
	handler, _, err := objectstore.New(cfg)
	if err != nil {
		ts.Close()
		t.Fatalf("objectstore.New: %v", err)
	}
	mux.Handle("/", handler)
	t.Cleanup(ts.Close)

	return ts, ts.Client()
}

func TestE2E_UploadDownload_TextFile(t *testing.T) {
	ts, client := startE2EServer(t)
	ctx := context.Background()

	v := newValidator(t)
	const bucket = "e2e-text"
	const key = "hello.txt"
	const body = "Hello from the Postgres token validator e2e test!"

	// Ensure bucket via direct store call (presigned PUT needs the dir).
	store, err := objectstore.NewLocalStore(t.TempDir(), ts.URL, v)
	if err != nil {
		t.Fatal(err)
	}
	_ = store // we'll use the HTTP API instead

	// Issue a PUT token.
	putTok, err := v.Issue(ctx, tokenstore.IssueRequest{
		Method:  "PUT",
		Bucket:  bucket,
		Key:     key,
		Expires: 5 * time.Minute,
		Tags:    map[string]string{"test": "text-upload", "format": "txt"},
	})
	if err != nil {
		t.Fatalf("Issue PUT: %v", err)
	}

	// Ensure bucket exists via presigned URL path (the file handler will create dirs).
	putURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=PUT&expires=" + itoa(putTok.ExpiresAt) + "&token=" + putTok.Token

	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}

	// Issue a GET token.
	getTok, err := v.Issue(ctx, tokenstore.IssueRequest{
		Method:  "GET",
		Bucket:  bucket,
		Key:     key,
		Expires: 5 * time.Minute,
		Tags:    map[string]string{"test": "text-download"},
	})
	if err != nil {
		t.Fatalf("Issue GET: %v", err)
	}

	getURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=GET&expires=" + itoa(getTok.ExpiresAt) + "&token=" + getTok.Token

	getResp, err := client.Get(getURL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getResp.StatusCode)
	}

	got, _ := io.ReadAll(getResp.Body)
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestE2E_UploadDownload_BinaryFile(t *testing.T) {
	ts, client := startE2EServer(t)
	ctx := context.Background()

	v := newValidator(t)
	const bucket = "e2e-binary"
	const key = "random.bin"

	// Generate 1 MB of random binary data.
	binaryData := make([]byte, 1<<20)
	if _, err := rand.Read(binaryData); err != nil {
		t.Fatal(err)
	}

	putTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "PUT", Bucket: bucket, Key: key, Expires: 5 * time.Minute,
		Tags: map[string]string{"test": "binary-upload", "size": "1MB"},
	})

	putURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=PUT&expires=" + itoa(putTok.ExpiresAt) + "&token=" + putTok.Token

	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(binaryData))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT binary: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	getTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: bucket, Key: key, Expires: 5 * time.Minute,
	})
	getURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=GET&expires=" + itoa(getTok.ExpiresAt) + "&token=" + getTok.Token

	getResp, err := client.Get(getURL)
	if err != nil {
		t.Fatalf("GET binary: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", getResp.StatusCode)
	}

	got, _ := io.ReadAll(getResp.Body)
	if !bytes.Equal(got, binaryData) {
		t.Fatalf("binary data mismatch: got %d bytes, want %d", len(got), len(binaryData))
	}
}

func TestE2E_UploadDownload_NestedPath(t *testing.T) {
	ts, client := startE2EServer(t)
	ctx := context.Background()

	v := newValidator(t)
	const bucket = "e2e-nested"
	const key = "documents/reports/2026/q1-summary.pdf"

	// Simulate a small PDF-like file (just binary with PDF magic bytes).
	pdfContent := append([]byte("%PDF-1.7\n"), make([]byte, 4096)...)

	putTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "PUT", Bucket: bucket, Key: key, Expires: 5 * time.Minute,
		Tags: map[string]string{"type": "pdf", "department": "finance", "year": "2026"},
	})

	putURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=PUT&expires=" + itoa(putTok.ExpiresAt) + "&token=" + putTok.Token

	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(pdfContent))
	req.Header.Set("Content-Type", "application/pdf")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT nested: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	getTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: bucket, Key: key, Expires: 5 * time.Minute,
	})
	getURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=GET&expires=" + itoa(getTok.ExpiresAt) + "&token=" + getTok.Token

	getResp, err := client.Get(getURL)
	if err != nil {
		t.Fatalf("GET nested: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", getResp.StatusCode)
	}

	got, _ := io.ReadAll(getResp.Body)
	if !bytes.Equal(got, pdfContent) {
		t.Fatalf("nested path file mismatch: got %d bytes, want %d", len(got), len(pdfContent))
	}
}

func TestE2E_OneTimeToken_Upload(t *testing.T) {
	ts, client := startE2EServer(t)
	ctx := context.Background()

	v := newValidator(t)
	const bucket = "e2e-onetime"
	const key = "single-use.txt"

	putTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "PUT", Bucket: bucket, Key: key, Expires: 5 * time.Minute, OneTime: true,
	})

	putURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=PUT&expires=" + itoa(putTok.ExpiresAt) + "&token=" + putTok.Token

	// First upload succeeds.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte("first")))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first PUT = %d, want 200", resp.StatusCode)
	}

	// Second upload with same token fails (one-time).
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte("second")))
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("second PUT = %d, want 403", resp2.StatusCode)
	}
}

func TestE2E_RevokedToken_Download(t *testing.T) {
	ts, client := startE2EServer(t)
	ctx := context.Background()

	v := newValidator(t)
	const bucket = "e2e-revoke"
	const key = "revokeme.txt"

	// Upload a file first.
	putTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "PUT", Bucket: bucket, Key: key, Expires: 5 * time.Minute,
	})
	putURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=PUT&expires=" + itoa(putTok.ExpiresAt) + "&token=" + putTok.Token
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader([]byte("revoke test")))
	resp, _ := client.Do(req)
	resp.Body.Close()

	// Issue GET token, then revoke it before use.
	getTok, _ := v.Issue(ctx, tokenstore.IssueRequest{
		Method: "GET", Bucket: bucket, Key: key, Expires: 5 * time.Minute,
	})
	if err := v.Revoke(ctx, getTok.Token); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	getURL := ts.URL + "/files/" + bucket + "/" + key +
		"?method=GET&expires=" + itoa(getTok.ExpiresAt) + "&token=" + getTok.Token
	getResp, err := client.Get(getURL)
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked GET = %d, want 403", getResp.StatusCode)
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
