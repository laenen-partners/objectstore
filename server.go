package objectstore

import (
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	"github.com/laenen-partners/objectstore/gen/objectstore/v1/objectstorev1connect"
)

// New creates a Store from config and returns an http.Handler that mounts
// both the connect-go ObjectStoreService and the REST file handler (for local backend).
// The handler includes request logging, security headers, and optional rate limiting and CORS.
func New(cfg Config) (http.Handler, Store, error) {
	store, err := NewStore(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("objectstore: create store: %w", err)
	}

	mux := http.NewServeMux()

	// Health check endpoints (unauthenticated).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Mount connect-go RPC handler with auth interceptor.
	var opts []connect.HandlerOption
	if len(cfg.APIKeys) > 0 {
		opts = append(opts, connect.WithInterceptors(NewAuthInterceptor(cfg.APIKeys)))
	}
	path, rpcHandler := objectstorev1connect.NewObjectStoreServiceHandler(
		NewHandler(store, cfg.MaxExpires), opts...,
	)
	mux.Handle(path, rpcHandler)

	// Mount file handler for local backend presigned URL upload/download.
	if ls, ok := store.(*LocalStore); ok {
		mux.Handle("/files/", NewFileHandler(ls, ls.TokenValidator()))
	}

	// Apply middleware stack: rate limiting (outermost) -> CORS -> logging -> security headers.
	var handler http.Handler = mux
	handler = SecurityHeaders(handler)
	handler = RequestLogging(handler)
	if len(cfg.CORSOrigins) > 0 {
		handler = CORS(cfg.CORSOrigins)(handler)
	}
	if cfg.RateLimit > 0 {
		handler = RateLimit(cfg.RateLimit, cfg.RateBurst)(handler)
	}

	return handler, store, nil
}
