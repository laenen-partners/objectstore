# Tiltfile for objectstore local development

# Start Postgres via Docker Compose
docker_compose("docker-compose.yml")

# Watch the objectstore server source and live-reload
local_resource(
    "objectstore-server",
    serve_cmd="export ADDR=:3000 OBJECT_STORE=file OBJECT_STORE_PATH=.data/objects OBJECT_STORE_URL=http://localhost:3000 OBJECT_STORE_POSTGRES_URL='postgres://objectstore:objectstore@localhost:5432/objectstore?sslmode=disable' && sleep 3 && go run ./cmd/objs",
    deps=[
        "cmd/objs",
        "*.go",
        "tokenstore/",
        "gen/",
    ],
    resource_deps=["postgres"],
)
