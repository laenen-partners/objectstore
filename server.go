package objectstore

import (
	"fmt"
	"net/http"

	"github.com/laenen-partners/objectstore/gen/objectstore/v1/objectstorev1connect"
)

// New creates a Store from config and returns an http.Handler that mounts
// both the connect-go ObjectStoreService and the REST file handler (for local backend).
func New(cfg Config) (http.Handler, Store, error) {
	store, err := NewStore(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("objectstore: create store: %w", err)
	}

	mux := http.NewServeMux()

	// Mount connect-go RPC handler.
	path, rpcHandler := objectstorev1connect.NewObjectStoreServiceHandler(NewHandler(store))
	mux.Handle(path, rpcHandler)

	// Mount file handler for local backend presigned URL upload/download.
	if ls, ok := store.(*LocalStore); ok {
		mux.Handle("/files/", NewFileHandler(ls, ls.TokenValidator()))
	}

	return mux, store, nil
}
