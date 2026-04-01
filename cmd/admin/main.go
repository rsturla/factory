// Command admin is the factory's cross-queue admin API server.
//
// Environment variables:
//
//	STORE_BACKEND       - "postgres", "dynamodb", or "sqlite" (default: "postgres")
//	DATABASE_URL        - PostgreSQL connection string (postgres backend)
//	DDB_TABLE           - DynamoDB table name (dynamodb backend)
//	S3_BUCKET           - S3 bucket for history (dynamodb backend)
//	SQLITE_PATH         - SQLite database path (sqlite backend)
//	AUTHN_BACKEND             - "noop" or "openshift" (default: "noop")
//	AUTHZ_BACKEND             - "noop", "cedar", or "opa" (default: "noop")
//	AUTHZ_CEDAR_POLICY_PATH   - Cedar policy file or directory (cedar backend)
//	AUTHZ_OPA_ENDPOINT        - OPA server URL (opa backend)
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

	"github.com/hummingbird-org/factory-workqueue/internal/admin"
	"github.com/hummingbird-org/factory-workqueue/internal/authn"
	"github.com/hummingbird-org/factory-workqueue/internal/authnutil"
	"github.com/hummingbird-org/factory-workqueue/internal/authzutil"
	"github.com/hummingbird-org/factory-workqueue/internal/metrics"
	"github.com/hummingbird-org/factory-workqueue/internal/storeutil"
	"github.com/hummingbird-org/factory-workqueue/internal/tracing"
)

func main() {
	listenAddr := envOr("LISTEN_ADDR", ":8080")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	shutdown := tracing.Init(ctx, "factory-admin")
	defer shutdown(context.Background())

	result, err := storeutil.CreateFromEnv(ctx)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}
	if result.Pool != nil {
		defer result.Pool.Close()
	}

	authenticator, err := authnutil.CreateFromEnv()
	if err != nil {
		slog.Error("failed to create authenticator", "error", err)
		os.Exit(1)
	}

	authorizer, err := authzutil.CreateFromEnv()
	if err != nil {
		slog.Error("failed to create authorizer", "error", err)
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	// Admin API routes — protected by authn + authz.
	adminMux := http.NewServeMux()
	admin.NewHandler(result.Store, authorizer).Register(adminMux)

	// Top-level mux: health/metrics are unauthenticated,
	// /admin/ routes go through authn middleware.
	mux := http.NewServeMux()
	mux.Handle("/admin/", authn.Middleware(authenticator)(adminMux))
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
