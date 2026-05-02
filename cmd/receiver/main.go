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
//	AUTHZ_BACKEND             - "noop", "cedar", or "opa" (default: "noop")
//	AUTHZ_CEDAR_POLICY_PATH   - Cedar policy file or directory (cedar backend)
//	AUTHZ_OPA_ENDPOINT        - OPA server URL (opa backend)
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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	"github.com/hummingbird-org/factory-workqueue/internal/authzutil"
	"github.com/hummingbird-org/factory-workqueue/internal/metrics"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/storeutil"
	"github.com/hummingbird-org/factory-workqueue/internal/tracing"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
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

	shutdown := tracing.Init(ctx, "factory-receiver")
	defer shutdown(context.Background())

	authorizer, err := authzutil.CreateFromEnv()
	if err != nil {
		slog.Error("failed to create authorizer", "error", err)
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	mux := http.NewServeMux()
	mux.Handle("POST /enqueue", authz.Wrap(authorizer, authz.ActionEnqueue, queueName,
		&enqueueHandler{queue: queueName, store: result.Store}))
	mux.Handle("POST /enqueue/batch", authz.Wrap(authorizer, authz.ActionEnqueue, queueName,
		&enqueueBatchHandler{queue: queueName, store: result.Store}))

	// Workqueue API — exposes store operations over HTTP for standalone workers.
	wqapi.NewHandler(result.Store, authorizer).Register(mux)
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
	ctx, span := tracing.Tracer("factory.receiver").Start(r.Context(), "enqueue",
		trace.WithAttributes(attribute.String("queue", h.queue)),
	)
	defer span.End()

	var req enqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	span.SetAttributes(attribute.String("key", req.Key), attribute.Int("priority", req.Priority))

	tracer := tracing.Tracer("factory.receiver")

	// Write to the store.
	func() {
		_, storeSpan := tracer.Start(ctx, "store.Enqueue")
		defer storeSpan.End()
		if err := h.store.Enqueue(ctx, h.queue, req.Key, req.Priority); err != nil {
			storeSpan.RecordError(err)
			span.RecordError(err)
			slog.Error("enqueue failed", "queue", h.queue, "key", req.Key, "error", err)
			http.Error(w, "enqueue failed", http.StatusInternalServerError)
			return
		}
	}()

	// Store W3C traceparent so the dispatcher can link its trace back
	// to this enqueue trace via a span link.
	sc := span.SpanContext()
	traceparent := fmt.Sprintf("00-%s-%s-01", sc.TraceID().String(), sc.SpanID().String())

	func() {
		_, histSpan := tracer.Start(ctx, "recordHistory")
		defer histSpan.End()
		h.store.RecordHistory(ctx, store.HistoryEntry{
			Queue:    h.queue,
			Key:      req.Key,
			ToStatus: "pending",
			TraceID:  traceparent,
		})
	}()

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	slog.Info("enqueued", "queue", h.queue, "key", req.Key, "priority", req.Priority, "trace_id", traceparent)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"enqueued","key":%q}`, req.Key)
}

type enqueueBatchHandler struct {
	queue string
	store store.Interface
}

type enqueueBatchRequest struct {
	Items []store.BatchEnqueueItem `json:"items"`
}

const maxBatchSize = 10000

func (h *enqueueBatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracing.Tracer("factory.receiver").Start(r.Context(), "enqueue_batch",
		trace.WithAttributes(attribute.String("queue", h.queue)),
	)
	defer span.End()

	var req enqueueBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Items) == 0 {
		http.Error(w, "items is required and must not be empty", http.StatusBadRequest)
		return
	}
	if len(req.Items) > maxBatchSize {
		http.Error(w, fmt.Sprintf("batch too large: %d items, max %d", len(req.Items), maxBatchSize), http.StatusRequestEntityTooLarge)
		return
	}
	for i, item := range req.Items {
		if item.Key == "" {
			http.Error(w, fmt.Sprintf("item[%d]: key is required", i), http.StatusBadRequest)
			return
		}
	}

	span.SetAttributes(attribute.Int("batch_size", len(req.Items)))

	tracer := tracing.Tracer("factory.receiver")
	var count int
	func() {
		_, storeSpan := tracer.Start(ctx, "store.EnqueueBatch")
		defer storeSpan.End()
		var err error
		count, err = h.store.EnqueueBatch(ctx, h.queue, req.Items)
		if err != nil {
			storeSpan.RecordError(err)
			span.RecordError(err)
			slog.Error("enqueue batch failed", "queue", h.queue, "count", len(req.Items), "error", err)
			http.Error(w, "enqueue batch failed", http.StatusInternalServerError)
			return
		}
	}()

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Add(float64(count))
	slog.Info("enqueued batch", "queue", h.queue, "requested", len(req.Items), "enqueued", count)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"enqueued","count":%d}`, count)
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
