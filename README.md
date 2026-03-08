# ObjectStore

Go library and server for object storage with a [Connect-RPC](https://connectrpc.com) API and presigned URL support. Plug in a local filesystem or AWS S3 / MinIO backend, choose between stateless HMAC or Postgres-backed token validation, and start uploading.

## Features

- **Two storage backends** -- local filesystem or S3-compatible (AWS, MinIO)
- **Connect-RPC API** -- protobuf-defined service with gRPC, gRPC-Web, and HTTP/JSON support
- **Presigned URLs** -- secure, time-limited upload and download links
- **Pluggable token validation** -- stateless HMAC-SHA256 with secret rotation, or Postgres-backed with revocation, one-time tokens, and tags
- **Automatic migrations** -- Postgres schema managed via embedded [dbmate](https://github.com/amacneil/dbmate) migrations
- **Use as a library or as a standalone server**

## Quick start

### Prerequisites

- [Go](https://go.dev) 1.25+
- [Task](https://taskfile.dev) (task runner)
- [Docker](https://docs.docker.com/get-docker/) (optional, for Postgres)

### Run the server

```sh
# Clone and enter the repo
git clone https://github.com/laenen-partners/objectstore.git
cd objectstore

# Configure
cp .env.sample .env
# Edit .env -- at minimum set OBJECT_STORE_SECRET

# Build and run
task build
./bin/objs
```

The server starts on `:3000` by default.

### Try it out

```sh
# Create a bucket
buf curl --protocol connect \
  http://localhost:3000/objectstore.v1.ObjectStoreService/EnsureBucket \
  -d '{"bucket": "my-bucket"}'

# Get a presigned upload URL
buf curl --protocol connect \
  http://localhost:3000/objectstore.v1.ObjectStoreService/PresignPut \
  -d '{"bucket": "my-bucket", "key": "hello.txt", "content_type": "text/plain"}'

# Upload using the returned URL
curl -X PUT "<presigned-url>" \
  -H "Content-Type: text/plain" \
  -d "Hello, ObjectStore!"

# Get a presigned download URL
buf curl --protocol connect \
  http://localhost:3000/objectstore.v1.ObjectStoreService/PresignGet \
  -d '{"bucket": "my-bucket", "key": "hello.txt"}'

# Download
curl "<presigned-url>"
```

## Use as a library

```go
import (
    "github.com/laenen-partners/objectstore"
    tokenhmac "github.com/laenen-partners/objectstore/tokenstore/hmac"
)

// Create an HMAC token validator
tokens, _ := tokenhmac.New("my-secret-key")

// Create a local filesystem store
store, _ := objectstore.NewLocalStore("/tmp/objects", "http://localhost:3000", tokens)

// Upload
store.PutObject(ctx, "my-bucket", "file.txt", reader, size, "text/plain")

// Presign a download URL
url, _ := store.PresignGet(ctx, "my-bucket", "file.txt", 15*time.Minute)
```

### With Postgres token validation

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

// One-time, tagged upload token
tok, _ := tokens.Issue(ctx, tokenstore.IssueRequest{
    Method:  "PUT",
    Bucket:  "uploads",
    Key:     "report.pdf",
    Expires: 10 * time.Minute,
    OneTime: true,
    Tags:    map[string]string{"user": "alice", "department": "finance"},
})

// Later: revoke all tokens for a user
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
| `ListByPrefix` | List object keys matching a prefix |

Presigned URLs are standard HTTP endpoints:

```
PUT /files/{bucket}/{key}?method=PUT&expires={unix}&token={token}
GET /files/{bucket}/{key}?method=GET&expires={unix}&token={token}
```

## Architecture

```
                          Connect-RPC
  Client ──────────────> /objectstore.v1.*  ──> Handler ──> Store
                                                              │
  Client (presigned) ──> /files/{bucket}/{key} ──> FileHandler
         PUT/GET              │                       │
                              │                       v
                              │               TokenValidator
                              │               ┌──────────────┐
                              │               │ HMAC (stateless)
                              │               │ Postgres (stateful)
                              │               └──────────────┘
                              v
                        ┌──────────────┐
                        │ LocalStore   │  or  S3Store
                        │ (filesystem) │     (AWS/MinIO)
                        └──────────────┘
```

### Token validation

| | HMAC | Postgres |
|---|---|---|
| Dependencies | None | PostgreSQL |
| Revocation | Not supported | Per-token and batch (by tags) |
| One-time tokens | Not supported | Supported |
| Tags | Ignored | Stored, searchable (JSONB + GIN index) |
| Secret rotation | Comma-separated secrets | N/A (opaque random tokens) |
| Token format | HMAC-SHA256 hex digest | 32-byte random hex |

## Configuration

All configuration is via environment variables. Copy `.env.sample` to `.env` to get started.

### Server

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3000` | Listen address |

### Storage backend

| Variable | Default | Description |
|---|---|---|
| `OBJECT_STORE` | `file` | Backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | File backend: storage directory |
| `OBJECT_STORE_URL` | `http://localhost:3000` | File backend: public base URL for presigned URLs |
| `OBJECT_STORE_SECRET` | | HMAC signing secret (comma-separated for key rotation) |

### Token store

| Variable | Default | Description |
|---|---|---|
| `TOKEN_STORE` | `hmac` | Token backend: `hmac` or `postgres` |
| `OBJECT_STORE_POSTGRES_URL` | | Postgres connection string (required when `TOKEN_STORE=postgres`) |

### S3 backend

| Variable | Default | Description |
|---|---|---|
| `S3_REGION` | | AWS region |
| `S3_ENDPOINT` | | Custom endpoint for MinIO or other S3-compatible services |

## Development

### Prerequisites

- [Go](https://go.dev) 1.25+
- [Task](https://taskfile.dev)
- [Buf](https://buf.build) (protobuf tooling)
- [sqlc](https://sqlc.dev) (SQL code generation)
- [Docker](https://docs.docker.com/get-docker/) (for Postgres)

### Setup

```sh
cp .env.sample .env

# Start Postgres
docker compose up -d

# Run all tests
task test

# Run Postgres integration tests
task test:postgres
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
task run             # run the server
task live            # run with live reload (requires air)
task tidy            # go mod tidy (both modules)
task clean           # remove bin/ and .data/
```

### Local dev with Tilt

```sh
tilt up
```

Starts Postgres via Docker Compose. Uncomment the `local_resource` blocks in `Tiltfile` to add live-reloading server and one-click test runners.

## Project structure

```
cmd/objs/                  Server binary
proto/                     Protobuf definitions
gen/                       Generated code (do not edit)
store.go                   Store interface
local.go                   LocalStore (filesystem)
s3.go                      S3Store (AWS/MinIO)
handler.go                 Connect-RPC handler
filehandler.go             Presigned URL HTTP handler
config.go                  Configuration
server.go                  Server wiring
e2e_file_test.go           End-to-end tests
tokenstore/
  tokenstore.go            TokenValidator interface
  hmac/                    Stateless HMAC-SHA256 validator
  postgres/                Postgres-backed validator (separate Go module)
    migrations/            Embedded dbmate migrations
    pgstore/               sqlc-generated code (do not edit)
docker-compose.yml         Postgres for local dev
Tiltfile                   Tilt orchestration
```

The `tokenstore/postgres` directory is a **separate Go module** to keep `pgx` and `dbmate` dependencies out of the core library. Both modules use `replace` directives for local development.

## License

See [LICENSE](LICENSE) for details.
