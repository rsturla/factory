// Package webhook provides HTTP handlers for receiving webhook events
// from GitHub and GitLab, extracting keys, and enqueuing them.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/store"
)

// KeyExtractor maps a webhook event to a queue key and priority.
type KeyExtractor func(eventType string, payload []byte) (key string, priority int, err error)

// Handler receives webhook events, verifies signatures, extracts keys, and enqueues them.
type Handler struct {
	queue     string
	store     store.Interface
	secret    string
	extractor KeyExtractor
}

// NewHandler creates a webhook handler for the given queue.
func NewHandler(queue string, s store.Interface, secret string, extractor KeyExtractor) http.Handler {
	return &Handler{queue: queue, store: s, secret: secret, extractor: extractor}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if h.secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, sig, h.secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "" {
		eventType = r.Header.Get("X-Gitlab-Event")
	}

	key, priority, err := h.extractor(eventType, body)
	if err != nil {
		slog.Error("key extraction failed", "queue", h.queue, "event", eventType, "error", err)
		http.Error(w, "key extraction failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if key == "" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"skipped"}`)
		return
	}

	if err := h.store.Enqueue(r.Context(), h.queue, key, priority); err != nil {
		slog.Error("enqueue failed", "queue", h.queue, "key", key, "error", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	slog.Info("enqueued from webhook", "queue", h.queue, "key", key, "priority", priority)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"enqueued","key":%q}`, key)
}

func verifySignature(body []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sigBytes, mac.Sum(nil))
}

// EnqueueHandler handles direct enqueue via HTTP POST.
type EnqueueHandler struct {
	queue string
	store store.Interface
}

// NewEnqueueHandler creates a handler for direct enqueue.
func NewEnqueueHandler(queue string, s store.Interface) http.Handler {
	return &EnqueueHandler{queue: queue, store: s}
}

type enqueueRequest struct {
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

func (h *EnqueueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"enqueued","key":%q}`, req.Key)
}
