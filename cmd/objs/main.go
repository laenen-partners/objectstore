package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start background token cleanup (every hour, removes tokens expired > 7 days ago).
	v.StartCleanup(ctx, 1*time.Hour)

	go func() {
		slog.Info("objectstore server starting", "addr", addr, "backend", cfg.Backend)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down gracefully")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}
