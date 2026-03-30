// Command receiver is the factory's generic webhook/enqueue service.
//
// Environment variables:
//
//	QUEUE_NAME        - The queue to enqueue into (required)
//	STORE_BACKEND     - "postgres", "dynamodb", or "sqlite" (default: "postgres")
//	DATABASE_URL      - PostgreSQL connection string (postgres backend)
//	DDB_TABLE         - DynamoDB table name (dynamodb backend)
//	S3_BUCKET         - S3 bucket for history (dynamodb backend)
//	SQLITE_PATH       - SQLite database path (sqlite backend)
//	WEBHOOK_SECRET    - HMAC secret for webhook signature verification
//	WEBHOOK_SOURCE    - "github" or "gitlab" (default: "github")
//	LISTEN_ADDR       - HTTP listen address (default: ":8081")
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

	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/storeutil"
	"github.com/hummingbird-org/factory/internal/webhook"
)

func main() {
	queueName := requireEnv("QUEUE_NAME")
	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	webhookSource := envOr("WEBHOOK_SOURCE", "github")
	listenAddr := envOr("LISTEN_ADDR", ":8081")

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

	var extractor webhook.KeyExtractor
	switch webhookSource {
	case "gitlab":
		extractor = webhook.GitLabKeyExtractor
	default:
		extractor = webhook.GitHubKeyExtractor
	}

	metrics.RegisterDefaults()

	mux := http.NewServeMux()
	mux.Handle("POST /webhook", webhook.NewHandler(queueName, result.Store, webhookSecret, extractor))
	mux.Handle("POST /enqueue", webhook.NewEnqueueHandler(queueName, result.Store))
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

	slog.Info("receiver starting", "queue", queueName, "addr", listenAddr, "source", webhookSource)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
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
