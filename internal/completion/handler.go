// Package completion provides the logic for deciding what happens after a
// reconciler finishes processing a work item — requeue with backoff, dead-letter,
// or complete.
package completion

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// Config controls retry behavior for a queue.
type Config struct {
	MaxAttempts    int
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	JitterFraction float64
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:    5,
		BackoffBase:    30 * time.Second,
		BackoffMax:     10 * time.Minute,
		JitterFraction: 0.25,
	}
}

// Handler processes the result of a reconciliation.
type Handler struct {
	store store.Interface
	cfg   Config
}

// NewHandler creates a new completion handler.
func NewHandler(s store.Interface, cfg Config) *Handler {
	return &Handler{store: s, cfg: cfg}
}

// HandleSuccess marks the work item as completed.
func (h *Handler) HandleSuccess(ctx context.Context, queue, key string) error {
	return h.store.Complete(ctx, queue, key)
}

// HandleFailure decides whether to requeue with backoff or dead-letter.
func (h *Handler) HandleFailure(ctx context.Context, queue, key string, attempt int, errMsg string) error {
	if err := h.store.Fail(ctx, queue, key, errMsg); err != nil {
		return err
	}
	if attempt >= h.cfg.MaxAttempts {
		return h.store.Deadletter(ctx, queue, key)
	}
	notBefore := time.Now().Add(h.backoff(attempt))
	return h.store.Requeue(ctx, queue, key, store.WithRequeueDelay(notBefore))
}

// HandleInfraFailure requeues without consuming retry budget.
func (h *Handler) HandleInfraFailure(ctx context.Context, queue, key string) error {
	notBefore := time.Now().Add(30 * time.Second)
	return h.store.RequeueUndoAttempt(ctx, queue, key, notBefore)
}

// HandleRequeueAfter requeues with a caller-specified delay.
func (h *Handler) HandleRequeueAfter(ctx context.Context, queue, key string, delay time.Duration) error {
	notBefore := time.Now().Add(delay)
	return h.store.RequeueUndoAttempt(ctx, queue, key, notBefore)
}

func (h *Handler) backoff(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	base := time.Duration(float64(h.cfg.BackoffBase) * exp)
	if base > h.cfg.BackoffMax {
		base = h.cfg.BackoffMax
	}
	jitter := time.Duration(float64(base) * h.cfg.JitterFraction * rand.Float64())
	return base + jitter
}
