# Build stage
FROM golang:1.25 AS build

WORKDIR /src

# Copy dependency files for both modules
COPY go.mod go.sum ./
COPY tokenstore/postgres/go.mod tokenstore/postgres/go.sum ./tokenstore/postgres/

# Download dependencies for both modules
RUN go mod download && \
    cd tokenstore/postgres && go mod download

# Copy source code
COPY . .

# Build static binary
RUN CGO_ENABLED=0 go build -o /objs ./cmd/objs

# Run stage
FROM gcr.io/distroless/static-debian12

COPY --from=build /objs /objs

EXPOSE 3000

ENTRYPOINT ["/objs"]
