// Package store defines the unified persistence interface for the factory platform.
//
// All durable state — work items, history, queue config, worker leases —
// flows through this single interface. Swapping the storage backend (e.g.,
// from PostgreSQL to CockroachDB or a custom store) requires implementing
// this interface alone.
//
// Data types are defined in pkg/types and re-exported here for backward
// compatibility. Consumers may import either package.
package store

import (
	"context"
	"time"

	"github.com/hummingbird-org/factory-workqueue/pkg/types"
)

// --- Type aliases (re-exported from pkg/types for backward compatibility) ---

type Status = types.Status

const (
	StatusPending    = types.StatusPending
	StatusClaimed    = types.StatusClaimed
	StatusRunning    = types.StatusRunning
	StatusSucceeded  = types.StatusSucceeded
	StatusFailed     = types.StatusFailed
	StatusDeadLetter = types.StatusDeadLetter
)

var (
	ErrNotFound          = types.ErrNotFound
	ErrConflict          = types.ErrConflict
	ErrInvalidTransition = types.ErrInvalidTransition
)

var ValidTransition = types.ValidTransition

type (
	WorkItem         = types.WorkItem
	QueueConfig      = types.QueueConfig
	QueueInfo        = types.QueueInfo
	ListFilter       = types.ListFilter
	HistoryEntry     = types.HistoryEntry
	WorkerLease      = types.WorkerLease
	Event            = types.Event
	ItemDetail       = types.ItemDetail
	BatchEnqueueItem = types.BatchEnqueueItem
)

type (
	EnqueueOption    = types.EnqueueOption
	EnqueueOpts      = types.EnqueueOpts
	RequeueOption    = types.RequeueOption
	RequeueOpts      = types.RequeueOpts
	TransitionOption = types.TransitionOption
	TransitionOpts   = types.TransitionOpts
)

var (
	WithNotBefore          = types.WithNotBefore
	ApplyEnqueueOptions    = types.ApplyEnqueueOptions
	WithRequeueDelay       = types.WithRequeueDelay
	ApplyRequeueOptions    = types.ApplyRequeueOptions
	WithWorkerID           = types.WithWorkerID
	WithErrorMessage       = types.WithErrorMessage
	ApplyTransitionOptions = types.ApplyTransitionOptions
)

// --- Sub-interfaces ---

// QueueWriter handles all mutations to work items: enqueue, claim,
// complete, fail, requeue, deadletter, and status transitions.
type QueueWriter interface {
	Enqueue(ctx context.Context, queue, key string, priority int, opts ...EnqueueOption) error
	EnqueueBatch(ctx context.Context, queue string, items []BatchEnqueueItem) (int, error)
	ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]WorkItem, error)
	Complete(ctx context.Context, queue, key string) error
	Fail(ctx context.Context, queue, key string, errMsg string) error
	Requeue(ctx context.Context, queue, key string, opts ...RequeueOption) error
	RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error
	Deadletter(ctx context.Context, queue, key string) error
	ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error
	Transition(ctx context.Context, queue, key string, from, to Status, opts ...TransitionOption) error
}

// QueueManager handles queue-level configuration and maintenance.
type QueueManager interface {
	EnsureQueue(ctx context.Context, queue string, cfg QueueConfig) error
	RepairCounter(ctx context.Context, queue string) error
	SetQueuePaused(ctx context.Context, queue string, paused bool) error
	IsQueuePaused(ctx context.Context, queue string) (bool, error)
}

// QueueReader handles all read operations: counting, listing, and
// fetching work items, queues, and workers.
type QueueReader interface {
	CountByStatus(ctx context.Context, queue string, statuses ...Status) (map[Status]int64, error)
	List(ctx context.Context, filter ListFilter) ([]WorkItem, error)
	ListExpiredLeases(ctx context.Context, queue string, limit int) ([]WorkItem, error)
	GetItem(ctx context.Context, queue, key string) (*WorkItem, error)
	ListQueues(ctx context.Context) ([]QueueInfo, error)
	ListWorkers(ctx context.Context, queue string) ([]WorkerLease, error)
	PurgeDeadLetters(ctx context.Context, queue string) (int64, error)
}

// HistoryRecorder handles audit trail operations.
type HistoryRecorder interface {
	RecordHistory(ctx context.Context, entry HistoryEntry) error
	GetItemHistory(ctx context.Context, queue, key string) ([]HistoryEntry, error)
}

// LeaderElector handles distributed leader election.
type LeaderElector interface {
	TryLeader(ctx context.Context, queue, workerID string, ttl time.Duration) (bool, error)
}

// EventSubscriber handles real-time event streaming.
type EventSubscriber interface {
	Subscribe(ctx context.Context, queue string) (<-chan Event, error)
}

// HealthChecker verifies backend connectivity.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Interface is the unified persistence contract for the factory platform.
// It embeds all sub-interfaces. Consumers that need only a subset of
// operations can accept the narrower sub-interface instead.
type Interface interface {
	QueueWriter
	QueueManager
	QueueReader
	HistoryRecorder
	LeaderElector
	EventSubscriber
	HealthChecker
}
