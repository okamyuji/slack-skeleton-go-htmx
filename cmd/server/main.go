// Package main slack-skeleton-go-htmxの起動エントリポイントです。
// Chapter 6時点ではSnapshotとMessageに加え、Hubを配線します。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/migrate"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/transport"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := envOr("APP_ADDR", ":8080")
	dsn := os.Getenv("DB_DSN")
	migrationsDir := envOr("MIGRATIONS_DIR", "migrations")

	deps, cleanup, err := buildDeps(logger, dsn, migrationsDir)
	if err != nil {
		logger.Error("init", "err", err)
		os.Exit(1)
	}
	defer cleanup()

	mux := transport.NewMux(deps)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped cleanly")
}

func buildDeps(logger *slog.Logger, dsn, migrationsDir string) (transport.Deps, func(), error) {
	r, err := render.New()
	if err != nil {
		return transport.Deps{}, func() {}, err
	}
	deps := transport.Deps{Logger: logger, Renderer: r}
	cleanup := func() {}

	if dsn == "" {
		logger.Warn("DB_DSN未設定。Snapshotは未配線で起動します")
		return deps, cleanup, nil
	}

	db, err := store.Open(dsn)
	if err != nil {
		return transport.Deps{}, func() {}, err
	}
	cleanup = func() { _ = db.Close() }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		cleanup()
		return transport.Deps{}, func() {}, err
	}
	if migrationsDir != "" {
		if err := migrate.Up(ctx, db, migrationsDir); err != nil {
			cleanup()
			return transport.Deps{}, func() {}, err
		}
	}

	s := store.New(db)
	deps.Snapshot = snapshot.New(s, 20)
	deps.Messages = message.New(s)
	deps.Hub = hub.New()
	return deps, cleanup, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
