package admin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory/internal/workqueue"
	"github.com/hummingbird-org/factory/internal/workqueue/postgres"
)

// Handler serves the admin API endpoints.
type Handler struct {
	queries *Queries
	wq      workqueue.Interface
	pool    *pgxpool.Pool
}

// NewHandler creates an admin API handler.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{
		queries: NewQueries(pool),
		wq:      postgres.New(pool),
		pool:    pool,
	}
}

// Register mounts all admin API routes on the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/queues", h.listQueues)
	mux.HandleFunc("GET /admin/queues/{name}", h.getQueue)
	mux.HandleFunc("GET /admin/queues/{name}/items", h.listItems)
	mux.HandleFunc("GET /admin/queues/{name}/items/{key}", h.getItem)
	mux.HandleFunc("POST /admin/queues/{name}/items/{key}/retry", h.retryItem)
	mux.HandleFunc("POST /admin/queues/{name}/items/{key}/cancel", h.cancelItem)
	mux.HandleFunc("DELETE /admin/queues/{name}/dead-letters", h.purgeDeadLetters)
	mux.HandleFunc("GET /admin/workers", h.listWorkers)
	mux.HandleFunc("GET /admin/queues/{name}/events", h.streamEvents)
}

func (h *Handler) listQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := h.queries.ListQueues(r.Context())
	if err != nil {
		serverError(w, "list queues", err)
		return
	}
	writeJSON(w, queues)
}

func (h *Handler) getQueue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	queues, err := h.queries.ListQueues(r.Context())
	if err != nil {
		serverError(w, "get queue", err)
		return
	}
	for _, q := range queues {
		if q.Name == name {
			writeJSON(w, q)
			return
		}
	}
	http.Error(w, "queue not found", http.StatusNotFound)
}

func (h *Handler) listItems(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	filter := workqueue.ListFilter{
		Queue: name,
		Limit: 100,
	}
	if s := r.URL.Query().Get("status"); s != "" {
		status := workqueue.Status(s)
		filter.Status = &status
	}

	items, err := h.wq.List(r.Context(), filter)
	if err != nil {
		serverError(w, "list items", err)
		return
	}
	if items == nil {
		items = []workqueue.WorkItem{}
	}
	writeJSON(w, items)
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")

	detail, err := h.queries.GetItem(r.Context(), name, key)
	if err != nil {
		serverError(w, "get item", err)
		return
	}
	writeJSON(w, detail)
}

func (h *Handler) retryItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")

	// Move from failed/dead_letter back to pending.
	err := h.wq.Requeue(r.Context(), name, key)
	if err != nil {
		// Try transitioning from dead_letter directly.
		err2 := h.wq.Transition(r.Context(), name, key, workqueue.StatusDeadLetter, workqueue.StatusPending)
		if err2 != nil {
			serverError(w, "retry item", err)
			return
		}
	}

	slog.Info("item retried via admin", "queue", name, "key", key)
	writeJSON(w, map[string]string{"status": "requeued"})
}

func (h *Handler) cancelItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")

	// Try cancelling from pending.
	err := h.wq.Transition(r.Context(), name, key, workqueue.StatusPending, workqueue.StatusFailed,
		workqueue.WithErrorMessage("cancelled via admin API"))
	if err != nil {
		// Try from claimed.
		err = h.wq.Fail(r.Context(), name, key, "cancelled via admin API")
		if err != nil {
			serverError(w, "cancel item", err)
			return
		}
	}

	slog.Info("item cancelled via admin", "queue", name, "key", key)
	writeJSON(w, map[string]string{"status": "cancelled"})
}

func (h *Handler) purgeDeadLetters(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	count, err := h.queries.PurgeDeadLetters(r.Context(), name)
	if err != nil {
		serverError(w, "purge dead letters", err)
		return
	}

	slog.Info("dead letters purged via admin", "queue", name, "count", count)
	writeJSON(w, map[string]any{"status": "purged", "count": count})
}

func (h *Handler) listWorkers(w http.ResponseWriter, r *http.Request) {
	queue := r.URL.Query().Get("queue")
	workers, err := h.queries.ListWorkers(r.Context(), queue)
	if err != nil {
		serverError(w, "list workers", err)
		return
	}
	if workers == nil {
		workers = []WorkerInfo{}
	}
	writeJSON(w, workers)
}

// streamEvents uses PostgreSQL LISTEN/NOTIFY to stream work item state changes
// as Server-Sent Events.
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	conn, err := h.pool.Acquire(r.Context())
	if err != nil {
		serverError(w, "acquire conn", err)
		return
	}
	defer conn.Release()

	channel := "work_item_" + name
	_, err = conn.Exec(r.Context(), "LISTEN "+channel)
	if err != nil {
		serverError(w, "listen", err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for {
		notification, err := conn.Conn().WaitForNotification(r.Context())
		if err != nil {
			return // client disconnected or context cancelled
		}
		fmt.Fprintf(w, "data: %s\n\n", notification.Payload)
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func serverError(w http.ResponseWriter, op string, err error) {
	slog.Error("admin api error", "op", op, "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
