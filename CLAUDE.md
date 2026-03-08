# ObjectStore

Go library and server for object storage with Connect-RPC API and presigned URL support.

See `~/.claude/CLAUDE.md` for org-wide Go service standards (tooling, security, conventions).

## Quick reference

- **Backends:** Local filesystem (`file`) or AWS S3 / MinIO (`s3`)
- **Token validation:** Postgres (revocable, one-time, tags, upload constraints)
- **Local dev:** Docker Compose + [Tilt](https://tilt.dev)

## Project structure

```
cmd/objs/                          Server binary (graceful shutdown)
proto/                             Protobuf definitions
gen/                               Generated protobuf/connect code (do not edit)
store.go                           Store interface, ObjectMeta, PresignPutParams
local.go                           LocalStore (filesystem backend, path-safe)
s3.go                              S3Store (AWS/MinIO backend)
handler.go                         Connect-RPC handler (auto-tags caller identity)
filehandler.go                     HTTP handler for presigned URL upload/download
auth.go                            API key auth interceptor (Connect unary)
caller.go                          Caller identity context propagation
validate.go                        Path traversal protection (safePath)
middleware.go                      HTTP middleware (logging, security headers, rate limiting, CORS)
config.go                          Config + factory
server.go                          New() wires everything together (health, auth, middleware)
e2e_file_test.go                   End-to-end tests (file backend)
tokenstore/
  tokenstore.go                    TokenValidator interface, types, sentinel errors
  postgres/                        Separate Go module (pgx, dbmate deps isolated)
    go.mod
    postgres.go                    Postgres-backed validator (WithMigrations option)
    postgres_test.go               Integration tests (requires OBJECT_STORE_POSTGRES_URL)
    migrations/                    dbmate migrations
    queries.sql                    sqlc query definitions
    sqlc.yaml
    pgstore/                       sqlc-generated code (do not edit)
mise.toml                          Dev tool versions (Go, Buf, sqlc, Task)
docker-compose.yml                 Postgres 17 for local dev
Tiltfile                           Tilt orchestration
docs/                              Review and design documents
```

## Service-specific commands

In addition to the standard tasks (see global CLAUDE.md):

```sh
task test:e2e        # run only e2e tests
task test:postgres   # Postgres integration tests (requires OBJECT_STORE_POSTGRES_URL)
task test:cover:html # HTML coverage report (opens in browser)
task live            # run with live reload (requires air)
```

## Service-specific environment

These env vars are specific to objectstore (in addition to the standard set):

| Variable | Default | Description |
|---|---|---|
| `OBJECT_STORE` | `file` | Backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | File backend: storage directory |
| `OBJECT_STORE_URL` | `http://localhost:3000` | File backend: public base URL |
| `OBJECT_STORE_POSTGRES_URL` | | Postgres connection string (required) |
| `S3_REGION` | | S3 backend: AWS region |
| `S3_ENDPOINT` | | S3 backend: custom endpoint (MinIO) |

## ObjectStore-specific details

- **Presigned URLs:** File handler (`/files/`) uses token-authenticated URLs for direct upload/download. Tokens self-authenticate — no API key needed.
- **Caller identity:** `X-User-ID` / `X-Service-ID` headers are auto-injected as `_user_id` / `_service_id` token tags for audit traceability.
- **One-time tokens:** Atomic `UPDATE ... WHERE used = FALSE RETURNING id` prevents race conditions.
- **Upload constraints:** Tokens enforce `MaxSize` (via `MaxBytesReader`) and `AllowedTypes` (415 on mismatch).
- **Content type persistence:** LocalStore uses `.objectstore-meta` sidecar JSON files.
- **S3 constraints:** S3 presigned PUT cannot enforce MaxSize/AllowedTypes server-side — a warning is logged.
- **Token cleanup:** Background goroutine deletes tokens expired > 7 days ago (configurable interval).
- **`tokenstore/postgres`** is a separate Go module to isolate pgx/dbmate dependencies.
- System-injected token tags use `_` prefix (e.g. `_user_id`) to distinguish from application tags.
