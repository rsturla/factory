// Command dispatcher is the factory's generic dispatch engine.
//
// Environment variables:
//
//	QUEUE_NAME            - The queue to dispatch (required)
//	DISPATCH_MODE         - "push" (default) or "scale-only"
//	RECONCILER_ENDPOINT   - Base URL of the reconciler service (required in push mode)
//	STORE_BACKEND         - "postgres", "dynamodb", or "sqlite" (default: "postgres")
//	DATABASE_URL          - PostgreSQL connection string (postgres backend)
//	DDB_TABLE             - DynamoDB table name (dynamodb backend)
//	S3_BUCKET             - S3 bucket for history (dynamodb backend)
//	DDB_ENDPOINT          - DynamoDB endpoint (optional, for local dev)
//	S3_ENDPOINT           - S3 endpoint (optional, for MinIO etc.)
//	SQLITE_PATH           - SQLite database path (sqlite backend)
//	WORKER_ID             - Unique identifier for this dispatcher (default: hostname)
//	COMPUTE_BACKEND       - "noop", "kubernetes", "ec2" (default: "noop")
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

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hummingbird-org/factory-workqueue/internal/compute"
	computeec2 "github.com/hummingbird-org/factory-workqueue/internal/compute/ec2"
	computek8s "github.com/hummingbird-org/factory-workqueue/internal/compute/kubernetes"
	"github.com/hummingbird-org/factory-workqueue/internal/dispatcher"
	"github.com/hummingbird-org/factory-workqueue/internal/metrics"
	"github.com/hummingbird-org/factory-workqueue/internal/storeutil"
	"github.com/hummingbird-org/factory-workqueue/internal/tracing"
	"github.com/hummingbird-org/factory-workqueue/pkg/client"
)

func main() {
	queueName := requireEnv("QUEUE_NAME")
	workerID := envOr("WORKER_ID", hostname())
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	dispatchMode := dispatcher.Mode(envOr("DISPATCH_MODE", "push"))

	cfg := dispatcher.DefaultConfig(queueName)
	cfg.WorkerID = workerID
	cfg.Mode = dispatchMode
	cfg.MaxConcurrency = envInt("MAX_CONCURRENCY", cfg.MaxConcurrency)
	cfg.MaxRetry = envInt("MAX_RETRY", cfg.MaxRetry)
	cfg.BatchSize = envInt("BATCH_SIZE", cfg.BatchSize)
	cfg.DispatchInterval = envDuration("DISPATCH_INTERVAL", cfg.DispatchInterval)
	cfg.LeaseDuration = envDuration("LEASE_DURATION", cfg.LeaseDuration)

	// RECONCILER_ENDPOINT is only required in push mode.
	reconcilerEndpoint := os.Getenv("RECONCILER_ENDPOINT")
	if dispatchMode == dispatcher.ModePush && reconcilerEndpoint == "" {
		slog.Error("RECONCILER_ENDPOINT required in push mode")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	shutdown := tracing.Init(ctx, "factory-dispatcher")
	defer shutdown(context.Background())

	result, err := storeutil.CreateFromEnv(ctx)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}
	if result.Pool != nil {
		defer result.Pool.Close()
	}

	reconciler := client.NewReconcilerClient(reconcilerEndpoint)

	var cp compute.Provider
	switch envOr("COMPUTE_BACKEND", "noop") {
	case "noop":
		cp = compute.NoopProvider{}
	case "kubernetes":
		var cpErr error
		cp, cpErr = computek8s.New(computek8s.Config{
			Namespace:        envOr("K8S_NAMESPACE", "factory"),
			DeploymentPrefix: envOr("K8S_DEPLOYMENT_PREFIX", "factory"),
			Kubeconfig:       os.Getenv("KUBECONFIG"),
		})
		if cpErr != nil {
			slog.Error("failed to create kubernetes provider", "error", cpErr)
			os.Exit(1)
		}
	case "ec2":
		var cpErr error
		cp, cpErr = computeec2.New(ctx, computeec2.Config{
			ASGPrefix: envOr("EC2_ASG_PREFIX", "factory"),
			Region:    os.Getenv("AWS_REGION"),
		})
		if cpErr != nil {
			slog.Error("failed to create ec2 provider", "error", cpErr)
			os.Exit(1)
		}
	default:
		slog.Error("unsupported compute backend", "backend", os.Getenv("COMPUTE_BACKEND"))
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	mux := http.NewServeMux()
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
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("health server error", "error", err)
		}
	}()

	d := dispatcher.New(result.Store, reconciler, cp, cfg)
	if err := d.Run(ctx); err != nil {
		slog.Error("dispatcher error", "error", err)
	}

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
		return fallback
	}
	return d
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "unknown"
	}
	return h
}
