// Command dispatcher is the factory's generic dispatch engine.
//
// It claims items from a PostgreSQL-backed work queue, invokes reconciler
// services over HTTP, and manages the full lifecycle of work items. One
// instance runs per reconciler queue (leader-elected singleton).
//
// Environment variables:
//
//	QUEUE_NAME            - The queue to dispatch (required)
//	DATABASE_URL          - PostgreSQL connection string (required)
//	RECONCILER_ENDPOINT   - Base URL of the reconciler service (required)
//	WORKER_ID             - Unique identifier for this dispatcher (default: hostname)
//	COMPUTE_BACKEND       - Compute provider: "noop", "kubernetes", "ec2" (default: "noop")
//	LISTEN_ADDR           - HTTP listen address for health/metrics (default: ":8080")
//	MAX_CONCURRENCY       - Maximum concurrent items (default: 10)
//	MAX_RETRY             - Maximum retry attempts (default: 5)
//	BATCH_SIZE            - Items to claim per dispatch cycle (default: 10)
//	DISPATCH_INTERVAL     - Dispatch loop interval (default: 2s)
//	LEASE_DURATION        - Lease duration for claimed items (default: 1h)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hummingbird-org/factory/internal/compute"
	computeec2 "github.com/hummingbird-org/factory/internal/compute/ec2"
	computek8s "github.com/hummingbird-org/factory/internal/compute/kubernetes"
	"github.com/hummingbird-org/factory/internal/dispatcher"
	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/workqueue/postgres"
	"github.com/hummingbird-org/factory/pkg/client"
)

func main() {
	queueName := requireEnv("QUEUE_NAME")
	databaseURL := requireEnv("DATABASE_URL")
	reconcilerEndpoint := requireEnv("RECONCILER_ENDPOINT")
	workerID := envOr("WORKER_ID", hostname())
	listenAddr := envOr("LISTEN_ADDR", ":8080")

	cfg := dispatcher.DefaultConfig(queueName)
	cfg.WorkerID = workerID
	cfg.MaxConcurrency = envInt("MAX_CONCURRENCY", cfg.MaxConcurrency)
	cfg.MaxRetry = envInt("MAX_RETRY", cfg.MaxRetry)
	cfg.BatchSize = envInt("BATCH_SIZE", cfg.BatchSize)
	cfg.DispatchInterval = envDuration("DISPATCH_INTERVAL", cfg.DispatchInterval)
	cfg.LeaseDuration = envDuration("LEASE_DURATION", cfg.LeaseDuration)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	wq := postgres.New(pool)

	// Run migrations on startup.
	if err := wq.Migrate(ctx); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	reconciler := client.NewReconcilerClient(reconcilerEndpoint)

	var cp compute.Provider
	switch envOr("COMPUTE_BACKEND", "noop") {
	case "noop":
		cp = compute.NoopProvider{}
	case "kubernetes":
		var err error
		cp, err = computek8s.New(computek8s.Config{
			Namespace:        envOr("K8S_NAMESPACE", "factory"),
			DeploymentPrefix: envOr("K8S_DEPLOYMENT_PREFIX", "factory"),
			Kubeconfig:       os.Getenv("KUBECONFIG"),
		})
		if err != nil {
			slog.Error("failed to create kubernetes provider", "error", err)
			os.Exit(1)
		}
	case "ec2":
		var err error
		cp, err = computeec2.New(ctx, computeec2.Config{
			ASGPrefix: envOr("EC2_ASG_PREFIX", "factory"),
			Region:    os.Getenv("AWS_REGION"),
		})
		if err != nil {
			slog.Error("failed to create ec2 provider", "error", err)
			os.Exit(1)
		}
	default:
		slog.Error("unsupported compute backend", "backend", os.Getenv("COMPUTE_BACKEND"))
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	// Start health/metrics server in the background.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unhealthy", http.StatusServiceUnavailable)
			return
		}
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
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("health server error", "error", err)
		}
	}()

	// Run the dispatcher (blocks until context is cancelled).
	d := dispatcher.New(wq, reconciler, cp, cfg)
	if err := d.Run(ctx); err != nil {
		slog.Error("dispatcher error", "error", err)
	}

	// Shutdown health server.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("invalid integer env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		slog.Warn("invalid duration env var, using default", "key", key, "value", v, "default", fallback)
		return fallback
	}
	return d
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
