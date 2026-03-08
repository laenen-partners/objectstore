package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/laenen-partners/objectstore"
	pgvalidator "github.com/laenen-partners/objectstore/tokenstore/postgres"
)

func main() {
	cfg := objectstore.ConfigFromEnv()

	if cfg.PostgresURL == "" {
		slog.Error("OBJECT_STORE_POSTGRES_URL is required")
		os.Exit(1)
	}

	v, err := pgvalidator.New(
		context.Background(),
		cfg.PostgresURL,
		pgvalidator.WithMigrations(),
	)
	if err != nil {
		slog.Error("failed to create postgres token validator", "error", err)
		os.Exit(1)
	}
	defer v.Close()
	cfg.TokenValidator = v

	handler, _, err := objectstore.New(cfg)
	if err != nil {
		slog.Error("failed to create objectstore", "error", err)
		os.Exit(1)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":3000"
	}

	slog.Info("objectstore server starting", "addr", addr, "backend", cfg.Backend)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
