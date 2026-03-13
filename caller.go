package objectstore

import "context"

type contextKey int

const callerKey contextKey = iota

// Caller represents the identity of the request originator.
// ServiceID identifies which upstream service is calling (from X-Service-ID header).
// UserID identifies the end user on whose behalf the request is made (from X-User-ID header).
type Caller struct {
	ServiceID string
	UserID    string
}

// CallerFromContext returns the Caller stored in ctx, or a zero Caller if none.
func CallerFromContext(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey).(Caller)
	return c
}

// WithCaller returns a new context with the given Caller stored in it.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey, c)
}
