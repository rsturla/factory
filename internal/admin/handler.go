// Package admin provides HTTP handlers for the cross-queue admin API.
package admin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// Handler serves the admin API endpoints.
type Handler struct {
	store store.Interface
	authz authz.Authorizer
}

// NewHandler creates an admin API handler.
func NewHandler(s store.Interface, a authz.Authorizer) *Handler {
	return &Handler{store: s, authz: a}
}

// Register mounts all admin API routes on the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	wrap := func(action authz.Action, handler http.HandlerFunc) http.Handler {
		return authz.Wrap(h.authz, action, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler(w, r)
		}))
	}
	wrapQueue := func(action authz.Action, handler http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			queue := r.PathValue("name")
			authz.Wrap(h.authz, action, queue, http.HandlerFunc(handler)).ServeHTTP(w, r)
		})
	}

	mux.Handle("GET /admin/queues", wrap(authz.ActionQueuesRead, h.listQueues))
	mux.Handle("GET /admin/queues/{name}", wrapQueue(authz.ActionQueuesRead, h.getQueue))
	mux.Handle("GET /admin/queues/{name}/items", wrapQueue(authz.ActionItemsRead, h.listItems))
	mux.Handle("GET /admin/queues/{name}/items/{key}", wrapQueue(authz.ActionItemsRead, h.getItem))
	mux.Handle("POST /admin/queues/{name}/items/{key}/retry", wrapQueue(authz.ActionItemsRetry, h.retryItem))
	mux.Handle("POST /admin/queues/{name}/items/{key}/cancel", wrapQueue(authz.ActionItemsCancel, h.cancelItem))
	mux.Handle("DELETE /admin/queues/{name}/dead-letters", wrapQueue(authz.ActionDeadLetterPurge, h.purgeDeadLetters))
	mux.Handle("GET /admin/workers", wrap(authz.ActionWorkersRead, h.listWorkers))
	mux.Handle("GET /admin/queues/{name}/events", wrapQueue(authz.ActionEventsStream, h.streamEvents))
}

func (h *Handler) listQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := h.store.ListQueues(r.Context())
	if err != nil {
		serverError(w, "list queues", err)
		return
	}
	writeJSON(w, queues)
}

func (h *Handler) getQueue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	queues, err := h.store.ListQueues(r.Context())
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
	filter := store.ListFilter{Queue: name, Limit: 100}
	if s := r.URL.Query().Get("status"); s != "" {
		status := store.Status(s)
		filter.Status = &status
	}
	items, err := h.store.List(r.Context(), filter)
	if err != nil {
		serverError(w, "list items", err)
		return
	}
	if items == nil {
		items = []store.WorkItem{}
	}
	writeJSON(w, items)
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")

	item, err := h.store.GetItem(r.Context(), name, key)
	if err != nil {
		serverError(w, "get item", err)
		return
	}
	history, err := h.store.GetItemHistory(r.Context(), name, key)
	if err != nil {
		serverError(w, "get history", err)
		return
	}
	writeJSON(w, store.ItemDetail{Item: *item, History: history})
}

func (h *Handler) retryItem(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")

	err := h.store.Requeue(r.Context(), name, key)
	if err != nil {
		err = h.store.Transition(r.Context(), name, key, store.StatusDeadLetter, store.StatusPending)
		if err != nil {
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

	err := h.store.Transition(r.Context(), name, key, store.StatusPending, store.StatusFailed,
		store.WithErrorMessage("cancelled via admin API"))
	if err != nil {
		err = h.store.Fail(r.Context(), name, key, "cancelled via admin API")
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
	count, err := h.store.PurgeDeadLetters(r.Context(), name)
	if err != nil {
		serverError(w, "purge dead letters", err)
		return
	}
	slog.Info("dead letters purged via admin", "queue", name, "count", count)
	writeJSON(w, map[string]any{"status": "purged", "count": count})
}

func (h *Handler) listWorkers(w http.ResponseWriter, r *http.Request) {
	queue := r.URL.Query().Get("queue")
	workers, err := h.store.ListWorkers(r.Context(), queue)
	if err != nil {
		serverError(w, "list workers", err)
		return
	}
	if workers == nil {
		workers = []store.WorkerLease{}
	}
	writeJSON(w, workers)
}

func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, err := h.store.Subscribe(r.Context(), name)
	if err != nil {
		serverError(w, "subscribe", err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for event := range ch {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func serverError(w http.ResponseWriter, op string, err error) {
	slog.Error("admin api error", "op", op, "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
