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
	store      Store
	maxExpires time.Duration
}

// NewHandler creates a connect-go RPC handler backed by the given Store.
// maxExpires caps the maximum presigned URL lifetime (0 = no cap).
func NewHandler(store Store, maxExpires time.Duration) *Handler {
	return &Handler{store: store, maxExpires: maxExpires}
}

func (h *Handler) PresignPut(ctx context.Context, req *connect.Request[objectstorev1.PresignPutRequest]) (*connect.Response[objectstorev1.PresignPutResponse], error) {
	params := PresignPutParams{
		Bucket:       req.Msg.Bucket,
		Key:          req.Msg.Key,
		ContentType:  req.Msg.ContentType,
		Expires:      h.capExpires(expiresDuration(req.Msg.ExpiresSeconds)),
		MaxSize:      req.Msg.MaxSize,
		AllowedTypes: req.Msg.AllowedTypes,
		Signature:    req.Msg.Signature,
		Scope:        req.Msg.Scope,
	}

	// Auto-inject caller identity into token tags for audit traceability.
	caller := CallerFromContext(ctx)
	if caller.UserID != "" || caller.ServiceID != "" {
		if params.Tags == nil {
			params.Tags = make(map[string]string)
		}
		if caller.UserID != "" {
			params.Tags["_user_id"] = caller.UserID
		}
		if caller.ServiceID != "" {
			params.Tags["_service_id"] = caller.ServiceID
		}
	}

	url, err := h.store.PresignPut(ctx, params)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.PresignPutResponse{Url: url}), nil
}

func (h *Handler) PresignGet(ctx context.Context, req *connect.Request[objectstorev1.PresignGetRequest]) (*connect.Response[objectstorev1.PresignGetResponse], error) {
	url, err := h.store.PresignGet(ctx, PresignGetParams{
		Bucket:   req.Msg.Bucket,
		Key:      req.Msg.Key,
		Expires:  h.capExpires(expiresDuration(req.Msg.ExpiresSeconds)),
		Filename: req.Msg.Filename,
	})
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
	result, err := h.store.ListByPrefix(ctx, req.Msg.Bucket, req.Msg.Prefix, int(req.Msg.PageSize), req.Msg.PageToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.ListByPrefixResponse{
		Keys:          result.Keys,
		NextPageToken: result.NextPageToken,
	}), nil
}

func (h *Handler) EnsureBucket(ctx context.Context, req *connect.Request[objectstorev1.EnsureBucketRequest]) (*connect.Response[objectstorev1.EnsureBucketResponse], error) {
	if err := h.store.EnsureBucket(ctx, req.Msg.Bucket); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&objectstorev1.EnsureBucketResponse{}), nil
}

// capExpires enforces the maximum presigned URL lifetime.
func (h *Handler) capExpires(d time.Duration) time.Duration {
	if h.maxExpires > 0 && d > h.maxExpires {
		return h.maxExpires
	}
	return d
}

func expiresDuration(seconds int32) time.Duration {
	if seconds <= 0 {
		return defaultExpireSeconds * time.Second
	}
	return time.Duration(seconds) * time.Second
}
