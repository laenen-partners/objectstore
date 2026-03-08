# Laenen Partners — Go Service Standards

These conventions apply to all Go microservices. Project-specific details belong in the repo's own `CLAUDE.md`.

To use in a new repo: copy this file to `~/.claude/CLAUDE.md` (or symlink it) so Claude Code auto-loads it for every project.

## Tooling

All repos use [mise](https://mise.jdx.dev) for tool management. The `mise.toml` in each repo is the single source of truth for tool versions.

**Required tools** (managed via mise):
- **Go** — version pinned per repo
- **Task** — [Taskfile](https://taskfile.dev) for commands (`Taskfile.yml`)
- **Buf** — protobuf linting and code generation
- **sqlc** — SQL code generation (repos with Postgres)

**Setup:**
```sh
mise install         # install all tools from mise.toml
cp .env.sample .env  # configure local environment
docker compose up -d # start dependencies (Postgres, etc.)
task test            # verify everything works
```

## Project layout

```
cmd/<name>/          Server binary entry point
proto/               Protobuf definitions
gen/                 Generated protobuf/connect code (never edit)
*_test.go            Tests alongside source files
tokenstore/          Token management (if applicable, separate Go module)
mise.toml            Dev tool versions
docker-compose.yml   Local dev dependencies
Tiltfile             Tilt orchestration (optional)
Dockerfile           Multi-stage build (distroless runtime)
.github/workflows/   CI via GitHub Actions (uses mise-action)
docs/                Design docs, review reports
```

## Common Taskfile commands

Every repo should have these tasks:

```sh
task generate        # buf generate (proto -> Go)
task generate:sqlc   # sqlc generate (if applicable)
task lint            # buf lint
task build           # go build -o bin/<name> ./cmd/<name>
task test            # go test -v -count=1 ./...
task test:cover      # run tests with coverage summary
task run             # run the server (reads .env via mise)
task tidy            # go mod tidy (all modules)
task clean           # remove build artifacts and local data
```

## Code conventions

- **No `init()` functions.** Wire dependencies explicitly in `main.go` or constructor functions.
- **Errors:** Wrap with `fmt.Errorf("context: %w", err)`.
- **Logging:** Use `slog` for structured logging. No `log.Fatal` outside `main()`.
- **Generated code:** Lives in `gen/` and any `pgstore/` directories — never edit manually.
- **Separate Go modules** for heavy dependency clusters (e.g. `tokenstore/postgres` isolates pgx/dbmate).
- **Context propagation:** Always pass `context.Context` as the first parameter.

## Security standards

- **Authentication:** Connect-RPC interceptor with API key auth (`Authorization: Bearer <key>`), constant-time comparison via `crypto/subtle`.
- **Caller identity:** Propagate `X-User-ID` and `X-Service-ID` headers into context; auto-inject into token tags with `_` prefix.
- **Path safety:** Validate all filesystem paths with a `safePath()` function that prevents traversal.
- **Security headers:** `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 0`, `Referrer-Policy: strict-origin-when-cross-origin`.
- **Rate limiting:** Per-IP token bucket middleware, configurable via `RATE_LIMIT` / `RATE_BURST`.
- **CORS:** Explicit origin allowlist via `CORS_ORIGINS` (comma-separated). No wildcards.
- **Graceful shutdown:** `signal.NotifyContext` with 30-second drain timeout.
- **TLS:** Terminate at reverse proxy or load balancer; plain HTTP in-cluster is acceptable.

## Environment variables

Standard env vars across all services (configure in `.env`, loaded by mise):

| Variable | Default | Description |
|---|---|---|
| `ADDR` | `:3000` | Server listen address |
| `API_KEYS` | | Comma-separated API keys for RPC auth |
| `RATE_LIMIT` | `10` | Requests per second per IP (0 = disabled) |
| `RATE_BURST` | `20` | Burst allowance per IP |
| `CORS_ORIGINS` | | Comma-separated allowed CORS origins |
| `MAX_EXPIRES_SECONDS` | `0` | Max presigned URL lifetime (0 = no cap) |

Service-specific env vars are documented in each repo's `CLAUDE.md` and `.env.sample`.

## CI/CD

All repos use GitHub Actions with `jdx/mise-action@v2` to install tools from `mise.toml`:

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: jdx/mise-action@v2
  - run: task lint
  - run: task build
  - run: go vet ./...
  - run: task test:cover
```

Dockerfiles use multi-stage builds with `gcr.io/distroless/static-debian12` as runtime image.

## Testing

- E2E tests use `httptest.Server` with real backends (no mocks for storage).
- Integration tests requiring external services (Postgres) are gated behind env vars.
- Target 70%+ statement coverage. Run `task test:cover` to check.
- Test naming: `TestE2E_<Feature>_<Scenario>` for end-to-end, `Test<Unit>_<Scenario>` for unit.

## Health endpoints

Every service exposes (unauthenticated, outside auth middleware):
- `GET /healthz` — liveness probe, returns `200 ok`
- `GET /readyz` — readiness probe, returns `200 ok`
