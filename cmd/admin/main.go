// Command admin is the factory's cross-queue admin API server.
//
// Environment variables:
//
//	STORE_BACKEND       - "postgres", "dynamodb", or "sqlite" (default: "postgres")
//	DATABASE_URL        - PostgreSQL connection string (postgres backend)
//	DDB_TABLE           - DynamoDB table name (dynamodb backend)
//	S3_BUCKET           - S3 bucket for history (dynamodb backend)
//	SQLITE_PATH         - SQLite database path (sqlite backend)
//	AUTHZ_BACKEND       - "noop", "headergroups", or "opa" (default: "noop")
//	AUTHZ_CONFIG_FILE   - Path to headergroups rules JSON
//	AUTHZ_OPA_ENDPOINT  - OPA server URL (e.g. "http://localhost:8181")
//	LISTEN_ADDR         - HTTP listen address (default: ":8080")
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hummingbird-org/factory/internal/admin"
	"github.com/hummingbird-org/factory/internal/authzutil"
	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/storeutil"
)

func main() {
	listenAddr := envOr("LISTEN_ADDR", ":8080")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	result, err := storeutil.CreateFromEnv(ctx)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}
	if result.Pool != nil {
		defer result.Pool.Close()
	}

	authorizer, err := authzutil.CreateFromEnv()
	if err != nil {
		slog.Error("failed to create authorizer", "error", err)
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	mux := http.NewServeMux()
	admin.NewHandler(result.Store, authorizer).Register(mux)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("admin api starting", "addr", listenAddr)
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
