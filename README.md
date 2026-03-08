# ObjectStore

Go library and server for object storage with a [Connect-RPC](https://connectrpc.com) API and presigned URL support. Plug in a local filesystem or AWS S3 / MinIO backend with Postgres-backed token validation.

## Features

- **Two storage backends** — local filesystem or S3-compatible (AWS, MinIO)
- **Connect-RPC API** — protobuf-defined service with gRPC, gRPC-Web, and HTTP/JSON support
- **Presigned URLs** — secure, time-limited upload and download links
- **Postgres token validation** — revocable, one-time tokens with tags and upload constraints
- **API key authentication** — service-to-service auth with caller identity propagation
- **Security hardened** — path traversal protection, rate limiting, security headers, CORS
- **Automatic migrations** — Postgres schema managed via embedded [dbmate](https://github.com/amacneil/dbmate) migrations
- **Cursor-based pagination** — for listing objects by prefix
- **Content-Disposition** — download filename support via presigned GET URLs

## Quick start

### Prerequisites

- [mise](https://mise.jdx.dev) (tool manager — installs Go, Task, Buf, sqlc)
- [Docker](https://docs.docker.com/get-docker/) (for Postgres)

### Setup and run

```sh
# Clone and enter the repo
git clone https://github.com/laenen-partners/objectstore.git
cd objectstore

# Install tools and configure
mise install
cp .env.sample .env

# Start Postgres and run the server
docker compose up -d
task run
```

The server starts on `:3000` by default.

### Try it out

```sh
# Create a bucket
buf curl --protocol connect \
  -H "Authorization: Bearer <your-api-key>" \
  http://localhost:3000/objectstore.v1.ObjectStoreService/EnsureBucket \
  -d '{"bucket": "my-bucket"}'

# Get a presigned upload URL
buf curl --protocol connect \
  -H "Authorization: Bearer <your-api-key>" \
  http://localhost:3000/objectstore.v1.ObjectStoreService/PresignPut \
  -d '{"bucket": "my-bucket", "key": "hello.txt", "content_type": "text/plain"}'

# Upload using the returned URL (no auth needed)
curl -X PUT "<presigned-url>" \
  -H "Content-Type: text/plain" \
  -d "Hello, ObjectStore!"

# Get a presigned download URL
buf curl --protocol connect \
  -H "Authorization: Bearer <your-api-key>" \
  http://localhost:3000/objectstore.v1.ObjectStoreService/PresignGet \
  -d '{"bucket": "my-bucket", "key": "hello.txt"}'

# Download (no auth needed)
curl "<presigned-url>"
```

> **Note:** When `API_KEYS` is empty (default for local dev), auth is disabled.

## Use as a library

```go
import (
    "github.com/laenen-partners/objectstore"
    pgvalidator "github.com/laenen-partners/objectstore/tokenstore/postgres"
)

// Create validator with automatic migrations
tokens, _ := pgvalidator.New(ctx, "postgres://user:pass@localhost/objectstore",
    pgvalidator.WithMigrations(),
)
defer tokens.Close()

store, _ := objectstore.NewLocalStore("/tmp/objects", "http://localhost:3000", tokens)

// Upload
store.PutObject(ctx, "my-bucket", "file.txt", reader, size, "text/plain")

// Presign a download URL with custom filename
url, _ := store.PresignGet(ctx, objectstore.PresignGetParams{
    Bucket:   "my-bucket",
    Key:      "file.txt",
    Expires:  15 * time.Minute,
    Filename: "download.txt",
})

// One-time, tagged upload token
tok, _ := tokens.Issue(ctx, tokenstore.IssueRequest{
    Method:  "PUT",
    Bucket:  "uploads",
    Key:     "report.pdf",
    Expires: 10 * time.Minute,
    OneTime: true,
    Tags:    map[string]string{"user": "alice", "department": "finance"},
})

// Revoke all tokens for a user
tokens.RevokeByTags(ctx, map[string]string{"user": "alice"})
```

## API reference

The Connect-RPC service is defined in [`proto/objectstore/v1/objectstore.proto`](proto/objectstore/v1/objectstore.proto).

| RPC | Description |
|-----|-------------|
| `EnsureBucket` | Create a bucket if it doesn't exist |
| `PresignPut` | Get a presigned URL for uploading an object |
| `PresignGet` | Get a presigned URL for downloading an object |
| `HeadObject` | Get object metadata (size, content type, etag) |
| `DeleteObject` | Delete an object |
| `ListByPrefix` | List object keys matching a prefix (paginated) |

### Presigned URL endpoints

```
PUT /files/{bucket}/{key}?method=PUT&expires={unix}&token={token}
GET /files/{bucket}/{key}?method=GET&expires={unix}&token={token}&filename={name}
```

### Health endpoints

```
GET /healthz    # liveness probe
GET /readyz     # readiness probe
```

## Architecture

```
                          Connect-RPC (auth interceptor)
  Client ──────────────> /objectstore.v1.*  ──> Handler ──> Store
                                                              │
  Client (presigned) ──> /files/{bucket}/{key} ──> FileHandler
         PUT/GET              │                       │
                              │                       v
                              │               TokenValidator
                              │               (Postgres-backed)
                              v
                        ┌──────────────┐
                        │ LocalStore   │  or  S3Store
                        │ (filesystem) │     (AWS/MinIO)
                        └──────────────┘
```

### Authentication flow

1. Calling service authenticates via `Authorization: Bearer <api-key>`
2. Caller identity headers (`X-User-ID`, `X-Service-ID`) are propagated into context
3. Identity is auto-injected as `_user_id` / `_service_id` tags on issued tokens
4. Presigned URLs self-authenticate via token — no API key needed

### Middleware stack

Rate limiting → CORS → Request logging → Security headers → Handler

## Configuration

All configuration is via environment variables. Copy `.env.sample` to `.env` to get started. Environment is loaded automatically by mise (see `mise.toml`).

### Server

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3000` | Listen address |
| `API_KEYS` | | Comma-separated API keys for RPC auth (empty = no auth) |
| `RATE_LIMIT` | `10` | Requests per second per IP (0 = disabled) |
| `RATE_BURST` | `20` | Burst allowance per IP |
| `CORS_ORIGINS` | | Comma-separated allowed CORS origins |
| `MAX_EXPIRES_SECONDS` | `0` | Max presigned URL lifetime in seconds (0 = no cap) |

### Storage backend

| Variable | Default | Description |
|---|---|---|
| `OBJECT_STORE` | `file` | Backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | File backend: storage directory |
| `OBJECT_STORE_URL` | `http://localhost:3000` | File backend: public base URL for presigned URLs |

### Token store

| Variable | Default | Description |
|---|---|---|
| `OBJECT_STORE_POSTGRES_URL` | | Postgres connection string (required) |

### S3 backend

| Variable | Default | Description |
|---|---|---|
| `S3_REGION` | | AWS region |
| `S3_ENDPOINT` | | Custom endpoint for MinIO or other S3-compatible services |

## Development

### Setup

```sh
mise install          # install Go, Task, Buf, sqlc
cp .env.sample .env
docker compose up -d  # start Postgres
task test             # run all tests
```

### Available tasks

```sh
task generate        # buf generate (proto -> Go)
task generate:sqlc   # sqlc generate (Postgres token store queries)
task lint            # buf lint protobuf definitions
task build           # go build -o bin/objs ./cmd/objs
task test            # go test ./... (unit + e2e)
task test:e2e        # run only e2e tests
task test:postgres   # run Postgres integration tests
task test:cover      # run tests with coverage summary
task test:cover:html # generate HTML coverage report
task run             # run the server
task live            # run with live reload (requires air)
task tidy            # go mod tidy (both modules)
task clean           # remove bin/ and .data/
```

### Local dev with Tilt

```sh
tilt up
```

Starts Postgres via Docker Compose and the server with live-reload on source changes.

### CI

GitHub Actions workflow runs on push to `main` and pull requests. Uses `jdx/mise-action` to install tools from `mise.toml`. See [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Project structure

```
cmd/objs/                  Server binary (graceful shutdown)
proto/                     Protobuf definitions
gen/                       Generated code (do not edit)
store.go                   Store interface, types
local.go                   LocalStore (filesystem)
s3.go                      S3Store (AWS/MinIO)
handler.go                 Connect-RPC handler
filehandler.go             Presigned URL HTTP handler
auth.go                    API key auth interceptor
caller.go                  Caller identity context propagation
validate.go                Path traversal protection
middleware.go              Logging, security headers, rate limiting, CORS
config.go                  Configuration
server.go                  Server wiring
e2e_file_test.go           End-to-end tests
tokenstore/
  tokenstore.go            TokenValidator interface
  postgres/                Postgres-backed validator (separate Go module)
    migrations/            Embedded dbmate migrations
    pgstore/               sqlc-generated code (do not edit)
mise.toml                  Dev tool versions
Dockerfile                 Multi-stage build (distroless)
docker-compose.yml         Postgres for local dev
Tiltfile                   Tilt orchestration
.github/workflows/         CI pipeline
docs/                      Design docs, review reports
```

The `tokenstore/postgres` directory is a **separate Go module** to keep `pgx` and `dbmate` dependencies out of the core library.

## License

See [LICENSE](LICENSE) for details.
