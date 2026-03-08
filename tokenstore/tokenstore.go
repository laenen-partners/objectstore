// Package tokenstore defines the interface for issuing and validating
// presigned URL tokens.
package tokenstore

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors returned by TokenValidator implementations.
var (
	ErrTokenInvalid       = errors.New("tokenstore: invalid token")
	ErrTokenExpired       = errors.New("tokenstore: token expired")
	ErrTokenRevoked       = errors.New("tokenstore: token revoked")
	ErrDuplicateSignature = errors.New("tokenstore: duplicate file signature in scope")
)

// IssueRequest describes a token to be created.
type IssueRequest struct {
	Method       string
	Bucket       string
	Key          string
	Expires      time.Duration
	OneTime      bool              // only meaningful for store-backed validators
	Tags         map[string]string // arbitrary key-value tags for search/batch ops
	MaxSize      int64             // 0 = server default
	AllowedTypes []string          // nil = any type allowed
	Signature    string            // SHA-256 content hash; empty = no uniqueness check
	Scope        string            // uniqueness boundary (e.g. "user:alice"); empty = global
}

// Token is the result of issuing a presigned URL token.
type Token struct {
	Token     string
	ExpiresAt int64 // unix timestamp
}

// Claims holds upload constraints returned by Validate so the file handler
// can enforce them.
type Claims struct {
	MaxSize      int64
	AllowedTypes []string
}

// TokenValidator issues, validates, and optionally revokes presigned URL tokens.
type TokenValidator interface {
	// Issue creates a new token for the given request.
	Issue(ctx context.Context, req IssueRequest) (*Token, error)

	// Validate checks whether a token is valid for the given parameters.
	// Returns Claims so the caller can enforce upload constraints.
	Validate(ctx context.Context, method, bucket, key string, expiresAt int64, token string) (*Claims, error)

	// Revoke invalidates a token.
	Revoke(ctx context.Context, token string) error
}
