// Package store defines the unified persistence interface for the factory platform.
//
// All durable state — work items, history, queue config, worker leases —
// flows through this single interface. Swapping the storage backend (e.g.,
// from PostgreSQL to CockroachDB or a custom store) requires implementing
// this interface alone.
package store

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

// Common errors returned by Interface implementations.
var (
	ErrNotFound = errors.New("work item not found")
	ErrConflict = errors.New("work item status conflict")
)

// Interface is the unified persistence contract for the factory platform.
// One implementation backs all platform operations: queue mechanics, history,
// admin queries, worker registration, and event streaming.
type Interface interface {
	// --- Work Queue Operations ---

	// Enqueue adds a key to the queue. If the key already exists in pending
	// status, the priority is merged upward. No payload — keys only.
	Enqueue(ctx context.Context, queue, key string, priority int, opts ...EnqueueOption) error

	// EnqueueBatch atomically enqueues multiple keys in a single round-trip.
	// Same dedup/priority-merge semantics as Enqueue. Returns count of items
	// actually enqueued or reactivated (in-flight items are skipped).
	EnqueueBatch(ctx context.Context, queue string, items []BatchEnqueueItem) (int, error)

	// ClaimBatch atomically claims up to batchSize pending items from a queue.
	// Items are ordered by priority DESC, created_at ASC.
	// Respects the queue's max_concurrency limit.
	ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]WorkItem, error)

	// Complete marks an item as succeeded and decrements the in-progress counter.
	Complete(ctx context.Context, queue, key string) error

	// Fail marks an item as failed.
	Fail(ctx context.Context, queue, key string, errMsg string) error

	// Requeue moves an item back to pending with an optional delay.
	Requeue(ctx context.Context, queue, key string, opts ...RequeueOption) error

	// RequeueUndoAttempt requeues without consuming retry budget.
	RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error

	// Deadletter moves an item to dead_letter status.
	Deadletter(ctx context.Context, queue, key string) error

	// ExtendLease extends the lease for a claimed or running item.
	ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error

	// Transition moves an item from one status to another.
	Transition(ctx context.Context, queue, key string, from, to Status, opts ...TransitionOption) error

	// --- Queue Management ---

	// EnsureQueue creates the queue config if it doesn't exist.
	EnsureQueue(ctx context.Context, queue string, cfg QueueConfig) error

	// RepairCounter reconciles the in_progress counter against actual rows.
	RepairCounter(ctx context.Context, queue string) error

	// SetQueuePaused pauses or resumes a queue. When paused, items can be
	// enqueued but the dispatcher will not claim them.
	SetQueuePaused(ctx context.Context, queue string, paused bool) error

	// IsQueuePaused returns whether a queue is paused.
	IsQueuePaused(ctx context.Context, queue string) (bool, error)

	// --- Query Operations ---

	// CountByStatus returns item counts grouped by status for a queue.
	// When statuses is empty, all statuses are counted.
	// When statuses is provided, only those statuses are counted.
	CountByStatus(ctx context.Context, queue string, statuses ...Status) (map[Status]int64, error)

	// List returns items matching the filter.
	List(ctx context.Context, filter ListFilter) ([]WorkItem, error)

	// GetItem returns a single work item by queue and key.
	GetItem(ctx context.Context, queue, key string) (*WorkItem, error)

	// --- Admin Queries ---

	// ListQueues returns all registered queues with item counts.
	ListQueues(ctx context.Context) ([]QueueInfo, error)

	// ListWorkers returns registered workers, optionally filtered by queue.
	ListWorkers(ctx context.Context, queue string) ([]WorkerLease, error)

	// PurgeDeadLetters deletes dead-lettered items for a queue.
	PurgeDeadLetters(ctx context.Context, queue string) (int64, error)

	// --- History ---

	// RecordHistory writes a state transition to the audit log.
	RecordHistory(ctx context.Context, entry HistoryEntry) error

	// GetItemHistory returns the transition history for an item.
	GetItemHistory(ctx context.Context, queue, key string) ([]HistoryEntry, error)

	// --- Leader Election ---

	// TryLeader attempts to become or renew leadership for a queue.
	// Returns true if this workerID is (or became) the leader.
	// The lease expires after ttl. Must be called periodically to renew.
	TryLeader(ctx context.Context, queue, workerID string, ttl time.Duration) (bool, error)

	// --- Events ---

	// Subscribe returns a channel that receives events for a queue.
	// The channel is closed when the context is cancelled.
	// Implementations may use polling, PG LISTEN/NOTIFY, or other mechanisms.
	Subscribe(ctx context.Context, queue string) (<-chan Event, error)
}

// --- Types ---

// WorkItem represents a single item in the work queue.
type WorkItem struct {
	Queue        string     `json:"queue"`
	Key          string     `json:"key"`
	Status       Status     `json:"status"`
	Priority     int        `json:"priority"`
	Attempts     int        `json:"attempts"`
	MaxAttempts  int        `json:"max_attempts"`
	NotBefore    *time.Time `json:"not_before,omitempty"`
	LeaseExpires *time.Time `json:"lease_expires,omitempty"`
	WorkerID     string     `json:"worker_id,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// QueueConfig holds configuration for a queue.
type QueueConfig struct {
	MaxConcurrency int    `json:"max_concurrency"`
	MaxRetry       int    `json:"max_retry"`
	ComputeBackend string `json:"compute_backend"`
}

// QueueInfo describes a queue and its current state.
type QueueInfo struct {
	Name           string         `json:"name"`
	MaxConcurrency int            `json:"max_concurrency"`
	MaxRetry       int            `json:"max_retry"`
	ComputeBackend string         `json:"compute_backend"`
	Paused         bool           `json:"paused"`
	InProgress     int            `json:"in_progress"`
	Counts         map[string]int `json:"counts"`
}

// ListFilter specifies criteria for listing work items.
type ListFilter struct {
	Queue  string  `json:"queue"`
	Status *Status `json:"status,omitempty"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}

// HistoryEntry represents a single state transition record.
type HistoryEntry struct {
	ID           int64     `json:"id"`
	Queue        string    `json:"queue"`
	Key          string    `json:"key"`
	FromStatus   string    `json:"from_status"`
	ToStatus     string    `json:"to_status"`
	WorkerID     string    `json:"worker_id,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	Attempt      int       `json:"attempt,omitempty"`
	TraceID      string    `json:"trace_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// WorkerLease describes a registered worker.
type WorkerLease struct {
	WorkerID       string    `json:"worker_id"`
	Queue          string    `json:"queue"`
	ComputeBackend string    `json:"compute_backend"`
	Hostname       string    `json:"hostname,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	ItemsProcessed int64     `json:"items_processed"`
	Status         string    `json:"status"`
}

// Event is emitted when a work item changes state.
type Event struct {
	Queue    string `json:"queue"`
	Key      string `json:"key"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
}

// ItemDetail is a work item with its history attached.
type ItemDetail struct {
	Item    WorkItem       `json:"item"`
	History []HistoryEntry `json:"history"`
}

// BatchEnqueueItem represents a single item in a batch enqueue request.
type BatchEnqueueItem struct {
	Key       string     `json:"key"`
	Priority  int        `json:"priority"`
	NotBefore *time.Time `json:"not_before,omitempty"`
}

// --- Options ---

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

// ApplyEnqueueOptions resolves the given options.
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

// ApplyRequeueOptions resolves the given options.
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

// ApplyTransitionOptions resolves the given options.
func ApplyTransitionOptions(opts []TransitionOption) TransitionOpts {
	var o transitionOptions
	for _, fn := range opts {
		fn(&o)
	}
	return TransitionOpts{WorkerID: o.workerID, ErrorMessage: o.errorMessage}
}
