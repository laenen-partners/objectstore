# ObjectStore

Go library for object storage with presigned URL support. Plug in a local filesystem or AWS S3 / MinIO backend with Postgres-backed token validation.

## Features

- **Two storage backends** — local filesystem or S3-compatible (AWS, MinIO)
- **Presigned URLs** — secure, time-limited upload and download links
- **Postgres token validation** — revocable, one-time tokens with tags and upload constraints
- **Automatic migrations** — Postgres schema managed via embedded [dbmate](https://github.com/amacneil/dbmate) migrations
- **Cursor-based pagination** — for listing objects by prefix
- **Content-Disposition** — download filename support via presigned GET URLs
- **Path traversal protection** — validated filesystem paths
- **Testcontainers** — integration tests use [testcontainers-go](https://golang.testcontainers.org/) for Postgres

## Install

```sh
go get github.com/laenen-partners/objectstore
```

## Usage

### With functional options (recommended)

```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/laenen-partners/objectstore"
)

// Create a pgx pool (you own the lifecycle)
pool, _ := pgxpool.New(ctx, "postgres://user:pass@localhost/objectstore")
defer pool.Close()

// Create an ObjectStore with options
store, _ := objectstore.New(
    objectstore.WithLocalBackend("/tmp/objects", "http://localhost:3000"),
    objectstore.WithPgxPool(pool),
    objectstore.WithAutoMigrate(),
    objectstore.WithPostgresURL("postgres://user:pass@localhost/objectstore"),
)
defer store.Close()

// Upload directly
store.PutObject(ctx, "my-bucket", "file.txt", reader, size, "text/plain")

// Presigned upload URL
url, _ := store.PresignPut(ctx, objectstore.PresignPutParams{
    Bucket:      "my-bucket",
    Key:         "report.pdf",
    ContentType: "application/pdf",
    Expires:     15 * time.Minute,
    MaxSize:     10 << 20, // 10 MB limit
    AllowedTypes: []string{"application/pdf"},
    Tags:        map[string]string{"user": "alice"},
})

// Presigned download URL with custom filename
url, _ := store.PresignGet(ctx, objectstore.PresignGetParams{
    Bucket:   "my-bucket",
    Key:      "report.pdf",
    Expires:  15 * time.Minute,
    Filename: "Q1-Report.pdf",
})

// Mount the file handler for presigned URL uploads/downloads
mux := http.NewServeMux()
mux.Handle("/files/", store.FileHandler())
```

### Using the Store interface directly

```go
import (
    "github.com/laenen-partners/objectstore"
    pgvalidator "github.com/laenen-partners/objectstore/tokenstore/postgres"
)

// Create a Postgres token validator
tokens, _ := pgvalidator.New(ctx, "postgres://user:pass@localhost/objectstore",
    pgvalidator.WithMigrations(),
)
defer tokens.Close()

// Create a local store
store, _ := objectstore.NewLocalStore("/tmp/objects", "http://localhost:3000", tokens)

// Or use NewFromPool if you have an existing pgx pool
tokens, _ := pgvalidator.NewFromPool(pool, "")
```

## Options

| Option | Description |
|---|---|
| `WithLocalBackend(basePath, baseURL)` | Use local filesystem backend |
| `WithS3Backend(region, endpoint)` | Use AWS S3 or MinIO backend |
| `WithPgxPool(pool)` | Set the pgx connection pool for token validation |
| `WithAutoMigrate()` | Run database migrations on startup |
| `WithPostgresURL(url)` | Postgres URL for migrations (required with `WithAutoMigrate`) |
| `WithCleanupInterval(d)` | Expired token cleanup interval (default: 1h, 0 = disabled) |
| `WithMaxExpires(d)` | Cap presigned URL lifetime |

## Presigned URL endpoints

When using the local backend, mount `store.FileHandler()` at `/files/`:

```
PUT /files/{bucket}/{key}?method=PUT&expires={unix}&token={token}
GET /files/{bucket}/{key}?method=GET&expires={unix}&token={token}&filename={name}
```

## Development

```sh
mise install          # install Go, Task, sqlc
task test             # run all tests (uses testcontainers for Postgres)
task test:postgres    # run only Postgres integration tests
task generate:sqlc    # regenerate sqlc code
task tidy             # go mod tidy
```

## Project structure

```
objectstore.go         New() constructor with functional options
store.go               Store interface, types
local.go               LocalStore (filesystem backend)
s3.go                  S3Store (AWS/MinIO backend)
filehandler.go         Presigned URL HTTP handler
caller.go              Caller identity context propagation
validate.go            Path traversal protection
e2e_file_test.go       End-to-end tests
tokenstore/
  tokenstore.go        TokenValidator interface
  postgres/
    postgres.go        Postgres-backed validator
    migrations/        Embedded dbmate migrations
    pgstore/           sqlc-generated code (do not edit)
    queries.sql        sqlc query definitions
    sqlc.yaml          sqlc configuration
```

## License

See [LICENSE](LICENSE) for details.
