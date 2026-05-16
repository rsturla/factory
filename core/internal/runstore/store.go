// Package runstore provides persistence for pipeline runs, stage runs, and audit events.
package runstore

import (
	"context"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// Store is the interface for pipeline run persistence.
// Implementations: postgres (production), inmem (tests).
type Store interface {
	// Pipeline runs
	CreateRun(ctx context.Context, run *v1.PipelineRun) error
	CreateRunWithOutbox(ctx context.Context, run *v1.PipelineRun, outbox OutboxEntry) error
	GetRun(ctx context.Context, id string) (*v1.PipelineRun, error)
	UpdateRun(ctx context.Context, run *v1.PipelineRun) error
	ListRuns(ctx context.Context, filters ListRunsFilters) ([]v1.PipelineRun, string, error)

	// Stage runs
	CreateStage(ctx context.Context, stage *v1.StageRun) error
	GetStage(ctx context.Context, id string) (*v1.StageRun, error)
	UpdateStage(ctx context.Context, stage *v1.StageRun) error
	ListStages(ctx context.Context, runID string) ([]v1.StageRun, error)

	// Audit events
	CreateAuditEvent(ctx context.Context, event *v1.AuditEvent) error
	ListAuditEvents(ctx context.Context, runID string) ([]v1.AuditEvent, error)

	// Outbox for reliable enqueueing
	OutboxEnqueue(ctx context.Context, entry OutboxEntry) error
	OutboxPoll(ctx context.Context, limit int) ([]OutboxEntry, error)
	OutboxMarkSent(ctx context.Context, ids []int64) error
}

// ListRunsFilters filters pipeline runs during list.
type ListRunsFilters struct {
	Phase      string // filter by phase
	Limit      int    // max results
	PageToken  string // pagination token
}

// OutboxEntry represents a pending enqueue operation.
type OutboxEntry struct {
	ID        int64
	Queue     string
	Key       string
	Priority  int
	Sent      bool
	CreatedAt time.Time
}
