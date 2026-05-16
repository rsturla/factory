// Package types defines the shared data types for the factory workqueue
// platform. These types are used by both internal store implementations
// and external clients (sdk/go/client, sdk/go/reconciler).
package types

import (
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

// Common errors returned by store implementations.
var (
	ErrNotFound          = errors.New("work item not found")
	ErrConflict          = errors.New("work item status conflict")
	ErrInvalidTransition = errors.New("invalid status transition")
)

var validTransitions = map[Status]map[Status]bool{
	StatusPending:    {StatusClaimed: true, StatusFailed: true},
	StatusClaimed:    {StatusRunning: true, StatusFailed: true, StatusPending: true},
	StatusRunning:    {StatusSucceeded: true, StatusFailed: true, StatusPending: true},
	StatusFailed:     {StatusPending: true, StatusDeadLetter: true},
	StatusDeadLetter: {StatusPending: true},
}

func ValidTransition(from, to Status) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

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
	MaxConcurrency int `json:"max_concurrency"`
	MaxRetry       int `json:"max_retry"`
	ClaimShards    int `json:"claim_shards,omitempty"`
}

// QueueInfo describes a queue and its current state.
type QueueInfo struct {
	Name           string         `json:"name"`
	MaxConcurrency int            `json:"max_concurrency"`
	MaxRetry       int            `json:"max_retry"`
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
	FromStatus   Status    `json:"from_status"`
	ToStatus     Status    `json:"to_status"`
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
