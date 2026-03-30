// Package completion provides the logic for deciding what happens after a
// reconciler finishes processing a work item — requeue with backoff, dead-letter,
// or complete.
package completion

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/hummingbird-org/factory/internal/workqueue"
)

// Config controls retry behavior for a queue.
type Config struct {
	// MaxAttempts is the maximum number of attempts before dead-lettering.
	MaxAttempts int

	// BackoffBase is the base duration for exponential backoff.
	BackoffBase time.Duration

	// BackoffMax is the maximum backoff duration.
	BackoffMax time.Duration

	// JitterFraction is the fraction of the backoff to add as jitter (e.g., 0.25).
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

// Handler processes the result of a reconciliation and applies the appropriate
// action to the work queue (complete, requeue with backoff, or dead-letter).
type Handler struct {
	wq  workqueue.Interface
	cfg Config
}

// NewHandler creates a new completion handler.
func NewHandler(wq workqueue.Interface, cfg Config) *Handler {
	return &Handler{wq: wq, cfg: cfg}
}

// HandleSuccess marks the work item as completed.
func (h *Handler) HandleSuccess(ctx context.Context, queue, key string) error {
	return h.wq.Complete(ctx, queue, key)
}

// HandleFailure decides whether to requeue with backoff or dead-letter
// based on the item's attempt count.
func (h *Handler) HandleFailure(ctx context.Context, queue, key string, attempt int, errMsg string) error {
	if err := h.wq.Fail(ctx, queue, key, errMsg); err != nil {
		return err
	}

	if attempt >= h.cfg.MaxAttempts {
		return h.wq.Deadletter(ctx, queue, key)
	}

	notBefore := time.Now().Add(h.backoff(attempt))
	return h.wq.Requeue(ctx, queue, key, workqueue.WithRequeueDelay(notBefore))
}

// HandleInfraFailure requeues without consuming retry budget.
// Used when the reconciler never ran (e.g., network error calling reconciler).
func (h *Handler) HandleInfraFailure(ctx context.Context, queue, key string) error {
	notBefore := time.Now().Add(30 * time.Second)
	return h.wq.RequeueUndoAttempt(ctx, queue, key, notBefore)
}

// HandleRequeueAfter requeues with a caller-specified delay.
// Does not consume retry budget.
func (h *Handler) HandleRequeueAfter(ctx context.Context, queue, key string, delay time.Duration) error {
	notBefore := time.Now().Add(delay)
	return h.wq.RequeueUndoAttempt(ctx, queue, key, notBefore)
}

// backoff computes the backoff duration for the given attempt number.
// Formula: min(base * 2^(attempt-1), max) + jitter
func (h *Handler) backoff(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	base := time.Duration(float64(h.cfg.BackoffBase) * exp)
	if base > h.cfg.BackoffMax {
		base = h.cfg.BackoffMax
	}

	jitter := time.Duration(float64(base) * h.cfg.JitterFraction * rand.Float64())
	return base + jitter
}
