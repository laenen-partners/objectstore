// Package postgres provides a Postgres-backed TokenValidator that supports
// revocation, one-time tokens, tags, and automatic schema migrations.
package postgres

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/laenen-partners/objectstore/tokenstore"
	"github.com/laenen-partners/objectstore/tokenstore/postgres/pgstore"

	"github.com/amacneil/dbmate/v2/pkg/dbmate"
	_ "github.com/amacneil/dbmate/v2/pkg/driver/postgres"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Option configures the Postgres validator.
type Option func(*options)

type options struct {
	migrate        bool
	migrationsTable string
}

// WithMigrations enables automatic schema migration on startup.
// The migration table defaults to "objectstore_schema_migrations".
func WithMigrations() Option {
	return func(o *options) {
		o.migrate = true
	}
}

// WithMigrationsTable overrides the migration tracking table name.
// Implies WithMigrations().
func WithMigrationsTable(table string) Option {
	return func(o *options) {
		o.migrate = true
		o.migrationsTable = table
	}
}

// Validator is a Postgres-backed token validator that supports revocation,
// one-time tokens, and tag-based search/batch operations.
type Validator struct {
	pool    *pgxpool.Pool
	queries *pgstore.Queries
}

// New creates a Postgres-backed TokenValidator that owns its own connection pool.
// Apply WithMigrations() to run embedded schema migrations on startup.
func New(ctx context.Context, databaseURL string, opts ...Option) (*Validator, error) {
	cfg := &options{
		migrationsTable: "objectstore_schema_migrations",
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.migrate {
		if err := runMigrations(databaseURL, cfg.migrationsTable); err != nil {
			return nil, fmt.Errorf("postgres: run migrations: %w", err)
		}
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	return &Validator{
		pool:    pool,
		queries: pgstore.New(pool),
	}, nil
}

// NewFromPool creates a Postgres-backed TokenValidator using an existing pgx pool.
// The caller retains ownership of the pool and is responsible for closing it.
// databaseURL is only required when WithMigrations is used (dbmate needs it); pass "" otherwise.
func NewFromPool(pool *pgxpool.Pool, databaseURL string, opts ...Option) (*Validator, error) {
	cfg := &options{
		migrationsTable: "objectstore_schema_migrations",
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.migrate {
		if err := runMigrations(databaseURL, cfg.migrationsTable); err != nil {
			return nil, fmt.Errorf("postgres: run migrations: %w", err)
		}
	}

	return &Validator{
		pool:    pool,
		queries: pgstore.New(pool),
	}, nil
}

// Close releases the connection pool.
func (v *Validator) Close() {
	v.pool.Close()
}

// Issue creates a new opaque token stored in Postgres.
func (v *Validator) Issue(ctx context.Context, req tokenstore.IssueRequest) (*tokenstore.Token, error) {
	// Check signature uniqueness before inserting.
	if req.Signature != "" {
		exists, err := v.queries.CheckSignatureExists(ctx, pgstore.CheckSignatureExistsParams{
			Scope:     req.Scope,
			Signature: req.Signature,
		})
		if err != nil {
			return nil, fmt.Errorf("postgres: check signature: %w", err)
		}
		if exists {
			return nil, tokenstore.ErrDuplicateSignature
		}
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("postgres: generate token: %w", err)
	}
	tokenStr := hex.EncodeToString(tokenBytes)

	expiresAt := time.Now().Add(req.Expires)

	tags, err := json.Marshal(req.Tags)
	if err != nil {
		return nil, fmt.Errorf("postgres: marshal tags: %w", err)
	}
	if req.Tags == nil {
		tags = []byte("{}")
	}

	allowedTypes := req.AllowedTypes
	if allowedTypes == nil {
		allowedTypes = []string{}
	}

	err = v.queries.InsertToken(ctx, pgstore.InsertTokenParams{
		Token:  tokenStr,
		Method: req.Method,
		Bucket: req.Bucket,
		Key:    req.Key,
		ExpiresAt: pgtype.Timestamptz{
			Time:  expiresAt,
			Valid: true,
		},
		OneTime:      req.OneTime,
		Tags:         tags,
		MaxSize:      req.MaxSize,
		AllowedTypes: allowedTypes,
		Signature:    req.Signature,
		Scope:        req.Scope,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: insert token: %w", err)
	}

	return &tokenstore.Token{
		Token:     tokenStr,
		ExpiresAt: expiresAt.Unix(),
	}, nil
}

// Validate checks a token against the database and returns upload constraints.
func (v *Validator) Validate(ctx context.Context, method, bucket, key string, _ int64, token string) (*tokenstore.Claims, error) {
	row, err := v.queries.ValidateToken(ctx, token)
	if err != nil {
		return nil, tokenstore.ErrTokenInvalid
	}

	if row.Revoked {
		return nil, tokenstore.ErrTokenRevoked
	}

	if row.ExpiresAt.Valid && time.Now().After(row.ExpiresAt.Time) {
		return nil, tokenstore.ErrTokenExpired
	}

	if row.Method != method || row.Bucket != bucket || row.Key != key {
		return nil, tokenstore.ErrTokenInvalid
	}

	if row.OneTime {
		if row.Used {
			return nil, tokenstore.ErrTokenInvalid
		}
		// Atomic consume: only one concurrent request can succeed.
		if _, err := v.queries.ConsumeOneTimeToken(ctx, token); err != nil {
			return nil, tokenstore.ErrTokenInvalid
		}
	}

	return &tokenstore.Claims{
		MaxSize:      row.MaxSize,
		AllowedTypes: row.AllowedTypes,
	}, nil
}

// Revoke marks a token as revoked.
func (v *Validator) Revoke(ctx context.Context, token string) error {
	return v.queries.RevokeToken(ctx, token)
}

// RevokeByTags revokes all active tokens matching the given tags.
// Returns the number of tokens revoked.
func (v *Validator) RevokeByTags(ctx context.Context, tags map[string]string) (int64, error) {
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return 0, fmt.Errorf("postgres: marshal tags: %w", err)
	}
	return v.queries.RevokeByTags(ctx, tagsJSON)
}

// FindByTags returns tokens matching the given tags.
func (v *Validator) FindByTags(ctx context.Context, tags map[string]string) ([]pgstore.FindByTagsRow, error) {
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("postgres: marshal tags: %w", err)
	}
	return v.queries.FindByTags(ctx, tagsJSON)
}

// StartCleanup runs a background goroutine that periodically deletes expired tokens.
// The goroutine stops when ctx is cancelled.
func (v *Validator) StartCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := v.queries.DeleteExpiredTokens(ctx)
				if err != nil {
					slog.Error("token cleanup failed", "error", err)
				} else if n > 0 {
					slog.Info("cleaned up expired tokens", "count", n)
				}
			}
		}
	}()
}

func runMigrations(databaseURL, migrationsTable string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database URL: %w", err)
	}

	db := dbmate.New(u)
	db.FS = migrations
	db.MigrationsDir = []string{"migrations"}
	db.MigrationsTableName = migrationsTable
	db.AutoDumpSchema = false

	return db.Migrate()
}
