// Command receiver is the factory's generic webhook/enqueue service.
//
// It accepts work requests via webhooks or direct API calls and enqueues
// keys into a PostgreSQL-backed work queue. One instance is deployed per
// reconciler queue, configured via environment variables.
//
// Environment variables:
//
//	QUEUE_NAME        - The queue to enqueue into (required)
//	DATABASE_URL      - PostgreSQL connection string (required)
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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/webhook"
	"github.com/hummingbird-org/factory/internal/workqueue/postgres"
)

func main() {
	queueName := requireEnv("QUEUE_NAME")
	databaseURL := requireEnv("DATABASE_URL")
	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	webhookSource := envOr("WEBHOOK_SOURCE", "github")
	listenAddr := envOr("LISTEN_ADDR", ":8081")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	wq := postgres.New(pool)

	// Select key extractor based on webhook source.
	var extractor webhook.KeyExtractor
	switch webhookSource {
	case "gitlab":
		extractor = webhook.GitLabKeyExtractor
	default:
		extractor = webhook.GitHubKeyExtractor
	}

	metrics.RegisterDefaults()

	mux := http.NewServeMux()
	mux.Handle("POST /webhook", webhook.NewHandler(queueName, wq, webhookSecret, extractor))
	mux.Handle("POST /enqueue", webhook.NewEnqueueHandler(queueName, wq))
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
	slog.Info("receiver stopped")
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
