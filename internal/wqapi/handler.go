// Package wqapi provides HTTP handlers for the workqueue API.
//
// This exposes store.Interface operations over HTTP so that standalone
// workers (EC2, bare metal) can interact with the workqueue without
// direct database access. The pkg/client.WorkqueueClient is the
// corresponding client.
//
// All endpoints accept JSON POST and return JSON responses. Authorization
// is enforced per-endpoint via the authz.Authorizer.
package wqapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// Handler serves the workqueue API endpoints.
type Handler struct {
	store store.Interface
	authz authz.Authorizer
}

// NewHandler creates a workqueue API handler.
func NewHandler(s store.Interface, a authz.Authorizer) *Handler {
	return &Handler{store: s, authz: a}
}

// Register mounts all workqueue API routes on the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	// Enqueue — also available on receiver, but included here for completeness.
	mux.Handle("POST /wq/enqueue", h.withAuthz(authz.ActionEnqueue, h.enqueue))
	mux.Handle("POST /wq/enqueue-batch", h.withAuthz(authz.ActionEnqueue, h.enqueueBatch))

	// Worker operations — used by standalone workers.
	mux.Handle("POST /wq/claim", h.withAuthz(authz.ActionEnqueue, h.claim))
	mux.Handle("POST /wq/complete", h.withAuthz(authz.ActionEnqueue, h.complete))
	mux.Handle("POST /wq/fail", h.withAuthz(authz.ActionEnqueue, h.fail))
	mux.Handle("POST /wq/heartbeat", h.withAuthz(authz.ActionEnqueue, h.heartbeat))
	mux.Handle("POST /wq/transition", h.withAuthz(authz.ActionEnqueue, h.transition))
	mux.Handle("POST /wq/requeue", h.withAuthz(authz.ActionEnqueue, h.requeue))
	mux.Handle("POST /wq/requeue-undo", h.withAuthz(authz.ActionEnqueue, h.requeueUndo))
	mux.Handle("POST /wq/deadletter", h.withAuthz(authz.ActionEnqueue, h.deadletter))

	// Query operations.
	mux.Handle("POST /wq/count", h.withAuthz(authz.ActionQueuesRead, h.count))
	mux.Handle("POST /wq/list", h.withAuthz(authz.ActionItemsRead, h.list))
	mux.Handle("POST /wq/get-item", h.withAuthz(authz.ActionItemsRead, h.getItem))
	mux.Handle("POST /wq/list-queues", h.withAuthz(authz.ActionQueuesRead, h.listQueues))
	mux.Handle("POST /wq/list-workers", h.withAuthz(authz.ActionWorkersRead, h.listWorkers))
	mux.Handle("POST /wq/get-history", h.withAuthz(authz.ActionItemsRead, h.getHistory))
	mux.Handle("POST /wq/purge-dead-letters", h.withAuthz(authz.ActionDeadLetterPurge, h.purgeDeadLetters))

	// Management operations.
	mux.Handle("POST /wq/ensure-queue", h.withAuthz(authz.ActionEnqueue, h.ensureQueue))
	mux.Handle("POST /wq/repair", h.withAuthz(authz.ActionEnqueue, h.repair))
	mux.Handle("POST /wq/record-history", h.withAuthz(authz.ActionEnqueue, h.recordHistory))
	mux.Handle("POST /wq/set-paused", h.withAuthz(authz.ActionItemsCancel, h.setPaused))
	mux.Handle("POST /wq/is-paused", h.withAuthz(authz.ActionQueuesRead, h.isPaused))
}

func (h *Handler) withAuthz(action authz.Action, handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract queue from the request body for queue-scoped authz.
		// We peek at the body without consuming it.
		queue := ""
		if r.Body != nil {
			var peek struct {
				Queue string `json:"queue"`
			}
			// Buffer the body so we can read it twice.
			body, _ := readBody(r)
			json.Unmarshal(body, &peek)
			queue = peek.Queue
			r.Body = newReadCloser(body)
		}
		authz.Wrap(h.authz, action, queue, http.HandlerFunc(handler)).ServeHTTP(w, r)
	})
}

// --- Request types ---

type queueKeyReq struct {
	Queue string `json:"queue"`
	Key   string `json:"key"`
}

type claimReq struct {
	Queue         string `json:"queue"`
	BatchSize     int    `json:"batch_size"`
	WorkerID      string `json:"worker_id"`
	LeaseDuration string `json:"lease_duration"`
}

type failReq struct {
	Queue string `json:"queue"`
	Key   string `json:"key"`
	Error string `json:"error"`
}

type heartbeatReq struct {
	Queue    string `json:"queue"`
	Key      string `json:"key"`
	Duration string `json:"duration"`
}

type transitionReq struct {
	Queue string       `json:"queue"`
	Key   string       `json:"key"`
	From  store.Status `json:"from"`
	To    store.Status `json:"to"`
}

type requeueUndoReq struct {
	Queue     string `json:"queue"`
	Key       string `json:"key"`
	NotBefore string `json:"not_before"`
}

type enqueueReq struct {
	Queue    string `json:"queue"`
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

type ensureQueueReq struct {
	Queue  string            `json:"queue"`
	Config store.QueueConfig `json:"config"`
}

type queueReq struct {
	Queue string `json:"queue"`
}

// --- Handlers ---

func (h *Handler) enqueue(w http.ResponseWriter, r *http.Request) {
	var req enqueueReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.Enqueue(r.Context(), req.Queue, req.Key, req.Priority); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) enqueueBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Queue string                   `json:"queue"`
		Items []store.BatchEnqueueItem `json:"items"`
	}
	if !decode(w, r, &req) {
		return
	}
	count, err := h.store.EnqueueBatch(r.Context(), req.Queue, req.Items)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"status": "ok", "count": count})
}

func (h *Handler) claim(w http.ResponseWriter, r *http.Request) {
	var req claimReq
	if !decode(w, r, &req) {
		return
	}
	lease, err := time.ParseDuration(req.LeaseDuration)
	if err != nil {
		lease = 1 * time.Hour
	}
	if req.BatchSize < 1 {
		req.BatchSize = 1
	}
	items, err := h.store.ClaimBatch(r.Context(), req.Queue, req.BatchSize, req.WorkerID, lease)
	if err != nil {
		serverError(w, err)
		return
	}
	if items == nil {
		items = []store.WorkItem{}
	}
	writeJSON(w, items)
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	var req queueKeyReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.Complete(r.Context(), req.Queue, req.Key); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request) {
	var req failReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.Fail(r.Context(), req.Queue, req.Key, req.Error); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatReq
	if !decode(w, r, &req) {
		return
	}
	dur, err := time.ParseDuration(req.Duration)
	if err != nil {
		dur = 1 * time.Hour
	}
	if err := h.store.ExtendLease(r.Context(), req.Queue, req.Key, dur); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request) {
	var req transitionReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.Transition(r.Context(), req.Queue, req.Key, req.From, req.To); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) requeue(w http.ResponseWriter, r *http.Request) {
	var req queueKeyReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.Requeue(r.Context(), req.Queue, req.Key); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) requeueUndo(w http.ResponseWriter, r *http.Request) {
	var req requeueUndoReq
	if !decode(w, r, &req) {
		return
	}
	nb, err := time.Parse(time.RFC3339, req.NotBefore)
	if err != nil {
		nb = time.Now().Add(30 * time.Second)
	}
	if err := h.store.RequeueUndoAttempt(r.Context(), req.Queue, req.Key, nb); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) deadletter(w http.ResponseWriter, r *http.Request) {
	var req queueKeyReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.Deadletter(r.Context(), req.Queue, req.Key); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) count(w http.ResponseWriter, r *http.Request) {
	var req queueReq
	if !decode(w, r, &req) {
		return
	}
	counts, err := h.store.CountByStatus(r.Context(), req.Queue)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, counts)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	var filter store.ListFilter
	if !decode(w, r, &filter) {
		return
	}
	items, err := h.store.List(r.Context(), filter)
	if err != nil {
		serverError(w, err)
		return
	}
	if items == nil {
		items = []store.WorkItem{}
	}
	writeJSON(w, items)
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	var req queueKeyReq
	if !decode(w, r, &req) {
		return
	}
	item, err := h.store.GetItem(r.Context(), req.Queue, req.Key)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, item)
}

func (h *Handler) listQueues(w http.ResponseWriter, r *http.Request) {
	queues, err := h.store.ListQueues(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, queues)
}

func (h *Handler) listWorkers(w http.ResponseWriter, r *http.Request) {
	var req queueReq
	if !decode(w, r, &req) {
		return
	}
	workers, err := h.store.ListWorkers(r.Context(), req.Queue)
	if err != nil {
		serverError(w, err)
		return
	}
	if workers == nil {
		workers = []store.WorkerLease{}
	}
	writeJSON(w, workers)
}

func (h *Handler) getHistory(w http.ResponseWriter, r *http.Request) {
	var req queueKeyReq
	if !decode(w, r, &req) {
		return
	}
	entries, err := h.store.GetItemHistory(r.Context(), req.Queue, req.Key)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, entries)
}

func (h *Handler) purgeDeadLetters(w http.ResponseWriter, r *http.Request) {
	var req queueReq
	if !decode(w, r, &req) {
		return
	}
	count, err := h.store.PurgeDeadLetters(r.Context(), req.Queue)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"count": count})
}

func (h *Handler) ensureQueue(w http.ResponseWriter, r *http.Request) {
	var req ensureQueueReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.EnsureQueue(r.Context(), req.Queue, req.Config); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) repair(w http.ResponseWriter, r *http.Request) {
	var req queueReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.RepairCounter(r.Context(), req.Queue); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) recordHistory(w http.ResponseWriter, r *http.Request) {
	var entry store.HistoryEntry
	if !decode(w, r, &entry) {
		return
	}
	if err := h.store.RecordHistory(r.Context(), entry); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) setPaused(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Queue  string `json:"queue"`
		Paused bool   `json:"paused"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.SetQueuePaused(r.Context(), req.Queue, req.Paused); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) isPaused(w http.ResponseWriter, r *http.Request) {
	var req queueReq
	if !decode(w, r, &req) {
		return
	}
	paused, err := h.store.IsQueuePaused(r.Context(), req.Queue)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"paused": paused})
}

// --- Helpers ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("wqapi error", "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func readBody(r *http.Request) ([]byte, error) {
	var buf [4096]byte
	var result []byte
	for {
		n, err := r.Body.Read(buf[:])
		result = append(result, buf[:n]...)
		if err != nil {
			break
		}
	}
	return result, nil
}

type readCloser struct {
	data []byte
	pos  int
}

func newReadCloser(data []byte) *readCloser {
	return &readCloser{data: data}
}

func (r *readCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, io.EOF
	}
	return n, nil
}

func (r *readCloser) Close() error { return nil }
