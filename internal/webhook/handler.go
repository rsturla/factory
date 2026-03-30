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
	"github.com/hummingbird-org/factory/internal/workqueue"
)

// KeyExtractor is a function that maps a webhook event to a queue key and priority.
// Return empty key to skip enqueuing.
type KeyExtractor func(eventType string, payload []byte) (key string, priority int, err error)

// Handler is an HTTP handler that receives webhook events, verifies signatures,
// extracts keys, and enqueues them.
type Handler struct {
	queue     string
	wq        workqueue.Interface
	secret    string
	extractor KeyExtractor
}

// NewHandler creates a webhook handler for the given queue.
// If secret is non-empty, GitHub-style HMAC-SHA256 signature verification is performed.
func NewHandler(queue string, wq workqueue.Interface, secret string, extractor KeyExtractor) http.Handler {
	return &Handler{
		queue:     queue,
		wq:        wq,
		secret:    secret,
		extractor: extractor,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Verify signature if secret is configured.
	if h.secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, sig, h.secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Determine event type from headers.
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
		// Extractor chose to skip this event.
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"skipped"}`)
		return
	}

	if err := h.wq.Enqueue(r.Context(), h.queue, key, priority); err != nil {
		slog.Error("enqueue failed", "queue", h.queue, "key", key, "error", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	slog.Info("enqueued from webhook", "queue", h.queue, "key", key, "priority", priority, "event", eventType)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"enqueued","key":%q}`, key)
}

// verifySignature checks the GitHub-style HMAC-SHA256 signature.
// Expected format: "sha256=<hex>"
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
	expected := mac.Sum(nil)
	return hmac.Equal(sigBytes, expected)
}

// EnqueueHandler is a simple HTTP handler for programmatic enqueue requests.
type EnqueueHandler struct {
	queue string
	wq    workqueue.Interface
}

// NewEnqueueHandler creates a handler for direct enqueue via HTTP POST.
func NewEnqueueHandler(queue string, wq workqueue.Interface) http.Handler {
	return &EnqueueHandler{queue: queue, wq: wq}
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

	if err := h.wq.Enqueue(r.Context(), h.queue, req.Key, req.Priority); err != nil {
		slog.Error("enqueue failed", "queue", h.queue, "key", req.Key, "error", err)
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}

	metrics.ItemsEnqueued.WithLabelValues(h.queue).Inc()
	slog.Info("enqueued via API", "queue", h.queue, "key", req.Key, "priority", req.Priority)

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"enqueued","key":%q}`, req.Key)
}
