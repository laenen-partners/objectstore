package objectstore

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	objectstorev1 "github.com/laenen-partners/objectstore/gen/objectstore/v1"
	"github.com/laenen-partners/objectstore/gen/objectstore/v1/objectstorev1connect"
)

const defaultExpireSeconds = 900

// Handler implements the connect-go ObjectStoreServiceHandler.
type Handler struct {
	objectstorev1connect.UnimplementedObjectStoreServiceHandler
	store Store
}

// NewHandler creates a connect-go RPC handler backed by the given Store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) PresignPut(ctx context.Context, req *connect.Request[objectstorev1.PresignPutRequest]) (*connect.Response[objectstorev1.PresignPutResponse], error) {
	url, err := h.store.PresignPut(ctx, PresignPutParams{
		Bucket:       req.Msg.Bucket,
		Key:          req.Msg.Key,
		ContentType:  req.Msg.ContentType,
		Expires:      expiresDuration(req.Msg.ExpiresSeconds),
		MaxSize:      req.Msg.MaxSize,
		AllowedTypes: req.Msg.AllowedTypes,
		Signature:    req.Msg.Signature,
		Scope:        req.Msg.Scope,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.PresignPutResponse{Url: url}), nil
}

func (h *Handler) PresignGet(ctx context.Context, req *connect.Request[objectstorev1.PresignGetRequest]) (*connect.Response[objectstorev1.PresignGetResponse], error) {
	expires := expiresDuration(req.Msg.ExpiresSeconds)
	url, err := h.store.PresignGet(ctx, req.Msg.Bucket, req.Msg.Key, expires)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.PresignGetResponse{Url: url}), nil
}

func (h *Handler) HeadObject(ctx context.Context, req *connect.Request[objectstorev1.HeadObjectRequest]) (*connect.Response[objectstorev1.HeadObjectResponse], error) {
	meta, err := h.store.HeadObject(ctx, req.Msg.Bucket, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&objectstorev1.HeadObjectResponse{
		Key:          meta.Key,
		Size:         meta.Size,
		ContentType:  meta.ContentType,
		LastModified: timestamppb.New(meta.LastModified),
		Etag:         meta.ETag,
	}), nil
}

func (h *Handler) DeleteObject(ctx context.Context, req *connect.Request[objectstorev1.DeleteObjectRequest]) (*connect.Response[objectstorev1.DeleteObjectResponse], error) {
	if err := h.store.DeleteObject(ctx, req.Msg.Bucket, req.Msg.Key); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.DeleteObjectResponse{}), nil
}

func (h *Handler) ListByPrefix(ctx context.Context, req *connect.Request[objectstorev1.ListByPrefixRequest]) (*connect.Response[objectstorev1.ListByPrefixResponse], error) {
	keys, err := h.store.ListByPrefix(ctx, req.Msg.Bucket, req.Msg.Prefix)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.ListByPrefixResponse{Keys: keys}), nil
}

func (h *Handler) EnsureBucket(ctx context.Context, req *connect.Request[objectstorev1.EnsureBucketRequest]) (*connect.Response[objectstorev1.EnsureBucketResponse], error) {
	if err := h.store.EnsureBucket(ctx, req.Msg.Bucket); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.EnsureBucketResponse{}), nil
}

func expiresDuration(seconds int32) time.Duration {
	if seconds <= 0 {
		return defaultExpireSeconds * time.Second
	}
	return time.Duration(seconds) * time.Second
}
