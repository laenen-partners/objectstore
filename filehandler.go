package objectstore

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/laenen-partners/objectstore/tokenstore"
)

const maxUploadSize = 100 << 20 // 100 MB

// NewFileHandler returns an http.Handler that serves GET (download) and
// accepts PUT (upload) requests for objects stored in the given Store.
// Requests must carry valid tokens in the query string, validated via the
// provided TokenValidator.
//
// Mount at "/files/" on the HTTP mux.
func NewFileHandler(store Store, tokens tokenstore.TokenValidator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucket, key, ok := ParseFilePath(r.URL.Path)
		if !ok {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		method := r.URL.Query().Get("method")
		expiresStr := r.URL.Query().Get("expires")
		token := r.URL.Query().Get("token")

		if method == "" || expiresStr == "" || token == "" {
			http.Error(w, "missing token parameters", http.StatusForbidden)
			return
		}

		// The token must match the HTTP method being used.
		var expectedMethod string
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			expectedMethod = "GET"
		case http.MethodPut:
			expectedMethod = "PUT"
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if method != expectedMethod {
			http.Error(w, "token method mismatch", http.StatusForbidden)
			return
		}

		expiresAt, err := strconv.ParseInt(expiresStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid expires parameter", http.StatusForbidden)
			return
		}

		claims, err := tokens.Validate(r.Context(), expectedMethod, bucket, key, expiresAt, token)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusForbidden)
			return
		}

		switch r.Method {
		case http.MethodGet, http.MethodHead:
			serveGet(w, r, store, bucket, key)
		case http.MethodPut:
			servePut(w, r, store, bucket, key, claims)
		}
	})
}

func serveGet(w http.ResponseWriter, r *http.Request, store Store, bucket, key string) {
	rc, err := store.GetObject(r.Context(), bucket, key)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer rc.Close()

	meta, err := store.HeadObject(r.Context(), bucket, key)
	if err == nil && meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}

	// Set Content-Disposition if filename is specified in the presigned URL.
	if filename := r.URL.Query().Get("filename"); filename != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}

	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		slog.Error("serving file download", "bucket", bucket, "key", key, "error", err)
	}
}

func servePut(w http.ResponseWriter, r *http.Request, store Store, bucket, key string, claims *tokenstore.Claims) {
	limit := int64(maxUploadSize)
	if claims != nil && claims.MaxSize > 0 && claims.MaxSize < limit {
		limit = claims.MaxSize
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()

	ct := r.Header.Get("Content-Type")
	if claims != nil && len(claims.AllowedTypes) > 0 && !slices.Contains(claims.AllowedTypes, ct) {
		http.Error(w, "content type not allowed", http.StatusUnsupportedMediaType)
		return
	}

	if err := store.PutObject(r.Context(), bucket, key, r.Body, r.ContentLength, ct); err != nil {
		slog.Error("serving file upload", "bucket", bucket, "key", key, "error", err)
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
