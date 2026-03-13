# ObjectStore

Go library for object storage with presigned URL support.

See `~/.claude/CLAUDE.md` for org-wide Go service standards (tooling, security, conventions).

## Quick reference

- **Backends:** Local filesystem (`file`) or AWS S3 / MinIO (`s3`)
- **Token validation:** Postgres (revocable, one-time, tags, upload constraints)
- **Tests:** Testcontainers for Postgres (no external dependencies needed)

## Project structure

```
objectstore.go                     New() constructor with functional options
store.go                           Store interface, ObjectMeta, PresignPutParams
local.go                           LocalStore (filesystem backend, path-safe)
s3.go                              S3Store (AWS/MinIO backend)
filehandler.go                     HTTP handler for presigned URL upload/download
caller.go                          Caller identity context propagation
validate.go                        Path traversal protection (safePath)
e2e_file_test.go                   End-to-end tests (file backend)
tokenstore/
  tokenstore.go                    TokenValidator interface, types, sentinel errors
  postgres/
    postgres.go                    Postgres-backed validator (New, NewFromPool, WithMigrations)
    postgres_test.go               Integration tests (testcontainers)
    migrations/                    Embedded SQL migrations (laenen-partners/migrate)
    queries.sql                    sqlc query definitions
    sqlc.yaml
    pgstore/                       sqlc-generated code (do not edit)
mise.toml                          Dev tool versions (Go, sqlc, Task)
```

## Commands

```sh
task test            # run all tests
task test:postgres   # Postgres integration tests (testcontainers)
task test:cover:html # HTML coverage report (opens in browser)
task generate:sqlc   # regenerate sqlc code
task tidy            # go mod tidy
```

## ObjectStore-specific details

- **Functional options:** `objectstore.New(ctx, WithLocalBackend(...), WithPgxPool(pool), WithAutoMigrate(), ...)`
- **Presigned URLs:** File handler (`/files/`) uses token-authenticated URLs for direct upload/download. Tokens self-authenticate — no API key needed.
- **Caller identity:** `WithCaller(ctx, Caller{...})` / `CallerFromContext(ctx)` for propagating identity.
- **One-time tokens:** Atomic `UPDATE ... WHERE used = FALSE RETURNING id` prevents race conditions.
- **Upload constraints:** Tokens enforce `MaxSize` (via `MaxBytesReader`) and `AllowedTypes` (415 on mismatch).
- **Content type persistence:** LocalStore uses `.objectstore-meta` sidecar JSON files.
- **S3 constraints:** S3 presigned PUT cannot enforce MaxSize/AllowedTypes server-side — a warning is logged.
- **Token cleanup:** Background goroutine deletes tokens expired > 7 days ago (configurable via `WithCleanupInterval`).
- **`NewFromPool`:** Use an existing pgx pool; caller retains ownership and lifecycle.
- **`New`:** Creates its own pgx pool from a connection string.
- System-injected token tags use `_` prefix (e.g. `_user_id`) to distinguish from application tags.
