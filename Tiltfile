# Tiltfile for objectstore local development

# Start Postgres via Docker Compose
docker_compose("docker-compose.yml")

# Watch the objectstore server source and live-reload
local_resource(
    "objectstore-server",
    serve_cmd="go run ./cmd/objs",
    deps=[
        "cmd/objs",
        "*.go",
        "tokenstore/",
        "gen/",
    ],
    resource_deps=["postgres"],
    env={
        "ADDR": ":3000",
        "OBJECT_STORE": "file",
        "OBJECT_STORE_PATH": ".data/objects",
        "OBJECT_STORE_URL": "http://localhost:3000",
        "OBJECT_STORE_POSTGRES_URL": "postgres://objectstore:objectstore@localhost:5432/objectstore?sslmode=disable",
    },
)

# Run tests on source changes
local_resource(
    "tests",
    cmd="go test -count=1 ./...",
    deps=[
        "*.go",
        "tokenstore/",
    ],
    auto_init=False,
    trigger_mode=TRIGGER_MODE_MANUAL,
)

# Run postgres integration tests
local_resource(
    "tests:postgres",
    cmd="cd tokenstore/postgres && go test -count=1 -v ./...",
    deps=[
        "tokenstore/postgres/",
    ],
    resource_deps=["postgres"],
    auto_init=False,
    trigger_mode=TRIGGER_MODE_MANUAL,
    env={
        "OBJECT_STORE_POSTGRES_URL": "postgres://objectstore:objectstore@localhost:5432/objectstore?sslmode=disable",
    },
)
