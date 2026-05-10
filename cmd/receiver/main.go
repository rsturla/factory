// Command receiver is the factory's generic enqueue service.
//
// Accepts work items via HTTP POST and writes keys to the queue.
// Webhook parsing is the responsibility of the calling service —
// each reconciler team's own service parses events and calls /enqueue.
//
// Environment variables:
//
//	FACTORY_QUEUE_NAME        - The queue to enqueue into (required)
//	STORE_BACKEND             - "postgres", "dynamodb", or "sqlite" (default: "postgres")
//	PG_DATABASE_URL           - PostgreSQL connection string (postgres backend)
//	DDB_TABLE                 - DynamoDB table name (dynamodb backend)
//	S3_BUCKET                 - S3 bucket for history (dynamodb backend)
//	SQLITE_PATH               - SQLite database path (sqlite backend)
//	AUTHN_BACKEND             - "noop" or "openshift" (default: "noop")
//	AUTHZ_BACKEND             - "noop", "cedar", or "opa" (default: "noop")
//	AUTHZ_CEDAR_POLICY_PATH   - Cedar policy file or directory (cedar backend)
//	AUTHZ_OPA_ENDPOINT        - OPA server URL (opa backend)
//	FACTORY_LISTEN_ADDR       - HTTP listen address (default: ":8081")
//	RECEIVER_MAX_QUEUE_DEPTH  - Max pending items before rejecting (default: 0 = unlimited)
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hummingbird-org/factory-workqueue/internal/authn"
	"github.com/hummingbird-org/factory-workqueue/internal/authnutil"
	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	"github.com/hummingbird-org/factory-workqueue/internal/authzutil"
	"github.com/hummingbird-org/factory-workqueue/internal/envutil"
	"github.com/hummingbird-org/factory-workqueue/internal/httputil"
	"github.com/hummingbird-org/factory-workqueue/internal/logging"
	"github.com/hummingbird-org/factory-workqueue/internal/metrics"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/storeutil"
	"github.com/hummingbird-org/factory-workqueue/internal/tracing"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
)

// maxRequestBodySize is the maximum allowed request body size (10 MiB).
const maxRequestBodySize = 10 * 1024 * 1024

func main() {
	logging.Init()

	queueName := envutil.Require("FACTORY_QUEUE_NAME")
	listenAddr := envutil.Or("FACTORY_LISTEN_ADDR", ":8081")

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

	authenticator, err := authnutil.CreateFromEnv()
	if err != nil {
		slog.Error("failed to create authenticator", "error", err)
		os.Exit(1)
	}

	metrics.RegisterDefaults()

	maxDepth := envutil.Int("RECEIVER_MAX_QUEUE_DEPTH", 0)

	// API routes — behind authn middleware.
	apiMux := http.NewServeMux()
	apiMux.Handle("POST /enqueue", authz.Wrap(authorizer, authz.ActionEnqueue, queueName,
		&enqueueHandler{queue: queueName, store: result.Store, maxDepth: maxDepth}))
	apiMux.Handle("POST /enqueue/batch", authz.Wrap(authorizer, authz.ActionEnqueue, queueName,
		&enqueueBatchHandler{queue: queueName, store: result.Store, maxDepth: maxDepth}))

	// Workqueue API — exposes store operations over HTTP for standalone workers.
	wqapi.NewHandler(result.Store, authorizer).Register(apiMux)

	// Top-level mux: health/metrics are unauthenticated,
	// API routes go through authn middleware.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := result.Store.Ping(r.Context()); err != nil {
			http.Error(w, "store unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("/", authn.Middleware(authenticator)(apiMux))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           httputil.SecurityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

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
	queue    string
	store    store.Interface
	maxDepth int
}

type enqueueRequest struct {
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

type enqueueResponse struct {
	Status string `json:"status"`
	Key    string `json:"key"`
}

func (h *enqueueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracing.Tracer("factory.receiver").Start(r.Context(), "enqueue",
		trace.WithAttributes(attribute.String("queue", h.queue)),
	)
	defer span.End()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

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

	if h.maxDepth > 0 {
		counts, err := h.store.CountByStatus(r.Context(), h.queue, store.StatusPending)
		if err == nil && counts[store.StatusPending] >= int64(h.maxDepth) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "queue at capacity", http.StatusTooManyRequests)
			return
		}
	}

	tracer := tracing.Tracer("factory.receiver")

	// Write to the store.
	_, storeSpan := tracer.Start(ctx, "store.Enqueue")
	err := h.store.Enqueue(ctx, h.queue, req.Key, req.Priority)
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.End()
		span.RecordError(err)
		slog.Error("enqueue failed", "queue", h.queue, "key", req.Key, "error", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	storeSpan.End()

	// Store W3C traceparent so the dispatcher can link its trace back
	// to this enqueue trace via a span link.
	sc := span.SpanContext()
	traceparent := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"

	_, histSpan := tracer.Start(ctx, "recordHistory")
	h.store.RecordHistory(ctx, store.HistoryEntry{
		Queue:    h.queue,
		Key:      req.Key,
		ToStatus: "pending",
		TraceID:  traceparent,
	})
	histSpan.End()

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	slog.Info("enqueued", "queue", h.queue, "key", req.Key, "priority", req.Priority, "trace_id", traceparent)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enqueueResponse{Status: "enqueued", Key: req.Key})
}

type enqueueBatchHandler struct {
	queue    string
	store    store.Interface
	maxDepth int
}

type enqueueBatchRequest struct {
	Items []store.BatchEnqueueItem `json:"items"`
}

const maxBatchSize = 10000

type enqueueBatchResponse struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

func (h *enqueueBatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracing.Tracer("factory.receiver").Start(r.Context(), "enqueue_batch",
		trace.WithAttributes(attribute.String("queue", h.queue)),
	)
	defer span.End()

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

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
		http.Error(w, "batch too large: max "+strconv.Itoa(maxBatchSize)+" items", http.StatusRequestEntityTooLarge)
		return
	}
	for i, item := range req.Items {
		if item.Key == "" {
			http.Error(w, "item["+strconv.Itoa(i)+"]: key is required", http.StatusBadRequest)
			return
		}
	}

	span.SetAttributes(attribute.Int("batch_size", len(req.Items)))

	if h.maxDepth > 0 {
		counts, err := h.store.CountByStatus(r.Context(), h.queue, store.StatusPending)
		if err == nil && counts[store.StatusPending] >= int64(h.maxDepth) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "queue at capacity", http.StatusTooManyRequests)
			return
		}
	}

	tracer := tracing.Tracer("factory.receiver")

	_, storeSpan := tracer.Start(ctx, "store.EnqueueBatch")
	count, err := h.store.EnqueueBatch(ctx, h.queue, req.Items)
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.End()
		span.RecordError(err)
		slog.Error("enqueue batch failed", "queue", h.queue, "count", len(req.Items), "error", err)
		http.Error(w, "enqueue batch failed", http.StatusInternalServerError)
		return
	}
	storeSpan.End()

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Add(float64(count))
	slog.Info("enqueued batch", "queue", h.queue, "requested", len(req.Items), "enqueued", count)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enqueueBatchResponse{Status: "enqueued", Count: count})
}
