// Package workqueue defines the core interface for the factory work queue.
//
// The work queue is a pure key-based queue backed by PostgreSQL. It stores
// only keys — no payloads. Reconcilers fetch state from their own source of
// truth at reconciliation time.
package workqueue

import (
	"context"
	"errors"
	"time"
)

// Status represents the state of a work item in its lifecycle.
type Status string

const (
	StatusPending    Status = "pending"
	StatusClaimed    Status = "claimed"
	StatusRunning    Status = "running"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
)

// WorkItem represents a single item in the work queue.
type WorkItem struct {
	Queue       string     `json:"queue"`
	Key         string     `json:"key"`
	Status      Status     `json:"status"`
	Priority    int        `json:"priority"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	NotBefore   *time.Time `json:"not_before,omitempty"`
	LeaseExpires *time.Time `json:"lease_expires,omitempty"`
	WorkerID    string     `json:"worker_id,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Common errors returned by Interface implementations.
var (
	ErrNotFound = errors.New("work item not found")
	ErrConflict = errors.New("work item status conflict")
)

// Interface defines the contract for work queue implementations.
// Both PostgreSQL (production) and in-memory (testing) backends implement this.
type Interface interface {
	// Enqueue adds a key to the queue. If the key already exists in pending
	// status, the priority is merged upward via GREATEST. No payload is stored.
	Enqueue(ctx context.Context, queue, key string, priority int, opts ...EnqueueOption) error

	// ClaimBatch atomically claims up to batchSize pending items from a queue.
	// Items are ordered by priority DESC, created_at ASC.
	// Respects the queue's max_concurrency limit.
	ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]WorkItem, error)

	// Complete marks an item as succeeded and decrements the in-progress counter.
	Complete(ctx context.Context, queue, key string) error

	// Fail marks an item as failed. The completion handler decides whether to
	// requeue (with backoff) or dead-letter based on attempt count.
	Fail(ctx context.Context, queue, key string, errMsg string) error

	// Requeue moves an item back to pending status with an optional delay.
	// Consumes retry budget (increments attempts is already done at claim time).
	Requeue(ctx context.Context, queue, key string, opts ...RequeueOption) error

	// RequeueUndoAttempt requeues without consuming retry budget.
	// Used for infrastructure failures where the reconciler never ran.
	RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error

	// Deadletter moves an item to dead_letter status.
	Deadletter(ctx context.Context, queue, key string) error

	// ExtendLease extends the lease expiration for a claimed or running item.
	ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error

	// Transition moves an item from one status to another.
	// Returns ErrNotFound if the item doesn't exist, or ErrConflict if
	// the current status doesn't match 'from'.
	Transition(ctx context.Context, queue, key string, from, to Status, opts ...TransitionOption) error

	// CountByStatus returns item counts grouped by status for a queue.
	CountByStatus(ctx context.Context, queue string) (map[Status]int64, error)

	// List returns items matching the given filter. Used by the admin API.
	List(ctx context.Context, filter ListFilter) ([]WorkItem, error)

	// RepairCounter reconciles the in_progress counter in queue_state
	// against the actual count of claimed+running rows.
	RepairCounter(ctx context.Context, queue string) error

	// EnsureQueue creates the queue_state row if it doesn't exist.
	EnsureQueue(ctx context.Context, queue string, cfg QueueConfig) error
}

// QueueConfig holds configuration for a queue.
type QueueConfig struct {
	MaxConcurrency int    `json:"max_concurrency"`
	MaxRetry       int    `json:"max_retry"`
	ComputeBackend string `json:"compute_backend"`
}

// ListFilter specifies criteria for listing work items.
type ListFilter struct {
	Queue    string  `json:"queue"`
	Status   *Status `json:"status,omitempty"`
	Limit    int     `json:"limit"`
	Offset   int     `json:"offset"`
}

// EnqueueOption configures an Enqueue call.
type EnqueueOption func(*enqueueOptions)

type enqueueOptions struct {
	notBefore *time.Time
}

// WithNotBefore schedules the item to become eligible after the given time.
func WithNotBefore(t time.Time) EnqueueOption {
	return func(o *enqueueOptions) {
		o.notBefore = &t
	}
}

// EnqueueOpts holds resolved enqueue options.
type EnqueueOpts struct {
	NotBefore *time.Time
}

// ApplyEnqueueOptions resolves the given options into an EnqueueOpts.
func ApplyEnqueueOptions(opts []EnqueueOption) EnqueueOpts {
	var o enqueueOptions
	for _, fn := range opts {
		fn(&o)
	}
	return EnqueueOpts{NotBefore: o.notBefore}
}

// RequeueOption configures a Requeue call.
type RequeueOption func(*requeueOptions)

type requeueOptions struct {
	notBefore *time.Time
}

// WithRequeueDelay schedules the requeued item to become eligible after the given time.
func WithRequeueDelay(t time.Time) RequeueOption {
	return func(o *requeueOptions) {
		o.notBefore = &t
	}
}

// RequeueOpts holds resolved requeue options.
type RequeueOpts struct {
	NotBefore *time.Time
}

// ApplyRequeueOptions resolves the given options into a RequeueOpts.
func ApplyRequeueOptions(opts []RequeueOption) RequeueOpts {
	var o requeueOptions
	for _, fn := range opts {
		fn(&o)
	}
	return RequeueOpts{NotBefore: o.notBefore}
}

// TransitionOption configures a Transition call.
type TransitionOption func(*transitionOptions)

type transitionOptions struct {
	workerID     string
	errorMessage string
}

// WithWorkerID sets the worker ID on the transition.
func WithWorkerID(id string) TransitionOption {
	return func(o *transitionOptions) {
		o.workerID = id
	}
}

// WithErrorMessage sets an error message on the transition.
func WithErrorMessage(msg string) TransitionOption {
	return func(o *transitionOptions) {
		o.errorMessage = msg
	}
}

// TransitionOpts holds resolved transition options.
type TransitionOpts struct {
	WorkerID     string
	ErrorMessage string
}

// ApplyTransitionOptions resolves the given options into a TransitionOpts.
func ApplyTransitionOptions(opts []TransitionOption) TransitionOpts {
	var o transitionOptions
	for _, fn := range opts {
		fn(&o)
	}
	return TransitionOpts{WorkerID: o.workerID, ErrorMessage: o.errorMessage}
}
