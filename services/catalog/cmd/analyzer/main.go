package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsturla/factory/services/catalog/internal/analyze"
	"github.com/rsturla/factory/services/catalog/internal/blob"
	"github.com/rsturla/factory/services/catalog/internal/store"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	listenAddr := envOr("LISTEN_ADDR", ":8082")
	databaseURL := envOr("DATABASE_URL", "postgres://localhost:5432/catalogdb?sslmode=disable")

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}

	blobCfg := blob.ConfigFromEnv()
	blobs, err := blob.New(ctx, blobCfg)
	if err != nil {
		slog.Error("create blob store", "error", err)
		os.Exit(1)
	}

	s := store.NewPGStore(pool)
	rec := analyze.NewReconciler(s, blobs)

	mux := http.NewServeMux()
	mux.Handle("POST /process", reconciler.ReconcilerHandler(rec.Reconcile))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      600 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	slog.Info("analyzer starting", "addr", listenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
