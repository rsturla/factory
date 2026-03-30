// Command receiver is the factory's generic enqueue service.
//
// Accepts work items via HTTP POST and writes keys to the queue.
// Webhook parsing is the responsibility of the calling service —
// each reconciler team's own service parses events and calls /enqueue.
//
// Environment variables:
//
//	QUEUE_NAME          - The queue to enqueue into (required)
//	STORE_BACKEND       - "postgres", "dynamodb", or "sqlite" (default: "postgres")
//	DATABASE_URL        - PostgreSQL connection string (postgres backend)
//	DDB_TABLE           - DynamoDB table name (dynamodb backend)
//	S3_BUCKET           - S3 bucket for history (dynamodb backend)
//	SQLITE_PATH         - SQLite database path (sqlite backend)
//	AUTHZ_BACKEND       - "noop", "headergroups", or "opa" (default: "noop")
//	AUTHZ_CONFIG_FILE   - Path to headergroups rules JSON
//	AUTHZ_OPA_ENDPOINT  - OPA server URL
//	LISTEN_ADDR         - HTTP listen address (default: ":8081")
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hummingbird-org/factory/internal/authz"
	"github.com/hummingbird-org/factory/internal/authzutil"
	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/store"
	"github.com/hummingbird-org/factory/internal/storeutil"
)

func main() {
	queueName := requireEnv("QUEUE_NAME")
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

	authorizer, err := authzutil.CreateFromEnv()
	if err != nil {
		slog.Error("failed to create authorizer", "error", err)
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	mux := http.NewServeMux()
	mux.Handle("POST /enqueue", authz.Wrap(authorizer, authz.ActionEnqueue, queueName,
		&enqueueHandler{queue: queueName, store: result.Store}))
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

	slog.Info("receiver starting", "queue", queueName, "addr", listenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

type enqueueHandler struct {
	queue string
	store store.Interface
}

type enqueueRequest struct {
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

func (h *enqueueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req enqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	if err := h.store.Enqueue(r.Context(), h.queue, req.Key, req.Priority); err != nil {
		slog.Error("enqueue failed", "queue", h.queue, "key", req.Key, "error", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	slog.Info("enqueued", "queue", h.queue, "key", req.Key, "priority", req.Priority)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"enqueued","key":%q}`, req.Key)
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
