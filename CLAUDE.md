# ObjectStore

Go library and server for object storage with Connect-RPC API and presigned URL support.

## Quick reference

- **Language:** Go 1.25+
- **RPC framework:** Connect-RPC (protobuf)
- **Backends:** Local filesystem (`file`) or AWS S3 / MinIO (`s3`)
- **Token validation:** Postgres (revocable, one-time, tags, upload constraints)
- **Task runner:** [Task](https://taskfile.dev) (`Taskfile.yml`)
- **Proto tool:** [Buf](https://buf.build) (`buf.yaml`, `buf.gen.yaml`)
- **SQL codegen:** [sqlc](https://sqlc.dev) (`tokenstore/postgres/sqlc.yaml`)
- **Local dev:** Docker Compose + [Tilt](https://tilt.dev)

## Project structure

```
cmd/objs/                          Server binary
proto/                             Protobuf definitions
gen/                               Generated protobuf/connect code (do not edit)
store.go                           Store interface, ObjectMeta, PresignPutParams
local.go                           LocalStore (filesystem backend)
s3.go                              S3Store (AWS/MinIO backend)
handler.go                         Connect-RPC handler
filehandler.go                     HTTP handler for presigned URL upload/download
config.go                          Config + factory
server.go                          New() wires everything together
e2e_file_test.go                   End-to-end tests (file backend)
tokenstore/
  tokenstore.go                    TokenValidator interface, types, sentinel errors
  postgres/                        Separate Go module (pgx, dbmate deps isolated)
    go.mod
    postgres.go                    Postgres-backed validator (WithMigrations option)
    postgres_test.go               Integration tests (requires OBJECT_STORE_POSTGRES_URL)
    migrations/
      001_create_tokens.sql
    queries.sql                    sqlc query definitions
    sqlc.yaml
    pgstore/                       sqlc-generated code (do not edit)
docker-compose.yml                 Postgres 17 for local dev
Tiltfile                           Tilt orchestration
```

## Common commands

```sh
task generate        # buf generate (proto -> Go)
task generate:sqlc   # sqlc generate (postgres token store)
task lint            # buf lint
task build           # go build -o bin/objs ./cmd/objs
task test            # go test -v -count=1 ./...
task test:e2e        # run only e2e tests
task test:postgres   # run Postgres integration tests (requires OBJECT_STORE_POSTGRES_URL)
task run             # run the server (reads .env)
task live            # run with live reload (requires air)
task tidy            # go mod tidy (both modules)
task clean           # rm bin/ and .data/
```

## Configuration

Copy `.env.sample` to `.env` and adjust values. Key variables:

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3000` | Server listen address |
| `OBJECT_STORE` | `file` | Backend: `file` or `s3` |
| `OBJECT_STORE_PATH` | `.data/objects` | File backend: storage directory |
| `OBJECT_STORE_URL` | `http://localhost:3000` | File backend: public base URL |
| `OBJECT_STORE_POSTGRES_URL` | | Postgres connection string (required) |
| `S3_REGION` | | S3 backend: AWS region |
| `S3_ENDPOINT` | | S3 backend: custom endpoint (MinIO) |

## Code conventions

- No `init()` functions; wire dependencies explicitly in `main.go` or `New()`.
- Generated code lives in `gen/` and `tokenstore/postgres/pgstore/` -- never edit manually.
- Regenerate with `task generate` (proto) or `task generate:sqlc` (sqlc).
- Tests use `httptest.Server` with the real LocalStore for e2e coverage.
- Errors are wrapped with `fmt.Errorf("context: %w", err)`.
- Use `slog` for structured logging.
- The `tokenstore/postgres` module is a separate Go module to isolate pgx/dbmate deps.
- Use `replace` directive in `tokenstore/postgres/go.mod` for local development.
