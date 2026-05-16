package inmem

import (
	"context"
	"fmt"
	"sync"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// Store implements runstore.Store using in-memory maps (for testing).
type Store struct {
	mu     sync.RWMutex
	runs   map[string]*v1.PipelineRun
	stages map[string]*v1.StageRun
	events map[string][]v1.AuditEvent // keyed by run_id
	outbox []runstore.OutboxEntry
	nextID int64
}

// New creates an in-memory run store.
func New() *Store {
	return &Store{
		runs:   make(map[string]*v1.PipelineRun),
		stages: make(map[string]*v1.StageRun),
		events: make(map[string][]v1.AuditEvent),
		outbox: []runstore.OutboxEntry{},
		nextID: 1,
	}
}

// CreateRun inserts a pipeline run.
func (s *Store) CreateRun(ctx context.Context, run *v1.PipelineRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.runs[run.ID]; exists {
		return fmt.Errorf("run already exists: %s", run.ID)
	}

	// deep copy
	copied := *run
	s.runs[run.ID] = &copied
	return nil
}

// CreateRunWithOutbox atomically creates run and outbox entry.
func (s *Store) CreateRunWithOutbox(ctx context.Context, run *v1.PipelineRun, outbox runstore.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.runs[run.ID]; exists {
		return fmt.Errorf("run already exists: %s", run.ID)
	}

	// Atomic operation (single mutex)
	copied := *run
	s.runs[run.ID] = &copied

	outbox.ID = s.nextID
	s.nextID++
	s.outbox = append(s.outbox, outbox)

	return nil
}

// GetRun retrieves a pipeline run.
func (s *Store) GetRun(ctx context.Context, id string) (*v1.PipelineRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	run, ok := s.runs[id]
	if !ok {
		return nil, fmt.Errorf("run not found: %s", id)
	}

	// deep copy
	copied := *run
	return &copied, nil
}

// UpdateRun updates a pipeline run.
func (s *Store) UpdateRun(ctx context.Context, run *v1.PipelineRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.runs[run.ID]; !exists {
		return fmt.Errorf("run not found: %s", run.ID)
	}

	// deep copy
	copied := *run
	s.runs[run.ID] = &copied
	return nil
}

// ListRuns retrieves pipeline runs with filtering.
func (s *Store) ListRuns(ctx context.Context, filters runstore.ListRunsFilters) ([]v1.PipelineRun, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := filters.Limit
	if limit == 0 {
		limit = 50
	}

	runs := []v1.PipelineRun{}
	for _, run := range s.runs {
		if filters.Phase != "" && run.Phase != filters.Phase {
			continue
		}
		runs = append(runs, *run)
	}

	// simple pagination: return first N
	nextToken := ""
	if len(runs) > limit {
		runs = runs[:limit]
		nextToken = runs[limit-1].ID
	}

	return runs, nextToken, nil
}

// CreateStage inserts a stage run.
func (s *Store) CreateStage(ctx context.Context, stage *v1.StageRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.stages[stage.ID]; exists {
		return fmt.Errorf("stage already exists: %s", stage.ID)
	}

	copied := *stage
	s.stages[stage.ID] = &copied
	return nil
}

// GetStage retrieves a stage run.
func (s *Store) GetStage(ctx context.Context, id string) (*v1.StageRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stage, ok := s.stages[id]
	if !ok {
		return nil, fmt.Errorf("stage not found: %s", id)
	}

	copied := *stage
	return &copied, nil
}

// UpdateStage updates a stage run.
func (s *Store) UpdateStage(ctx context.Context, stage *v1.StageRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.stages[stage.ID]; !exists {
		return fmt.Errorf("stage not found: %s", stage.ID)
	}

	copied := *stage
	s.stages[stage.ID] = &copied
	return nil
}

// ListStages retrieves all stages for a run.
func (s *Store) ListStages(ctx context.Context, runID string) ([]v1.StageRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stages := []v1.StageRun{}
	for _, stage := range s.stages {
		if stage.RunID == runID {
			stages = append(stages, *stage)
		}
	}

	return stages, nil
}

// CreateAuditEvent inserts an audit event.
func (s *Store) CreateAuditEvent(ctx context.Context, event *v1.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	copied := *event
	s.events[event.RunID] = append(s.events[event.RunID], copied)
	return nil
}

// ListAuditEvents retrieves all audit events for a run.
func (s *Store) ListAuditEvents(ctx context.Context, runID string) ([]v1.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	events := s.events[runID]
	if events == nil {
		return []v1.AuditEvent{}, nil
	}

	// deep copy
	copied := make([]v1.AuditEvent, len(events))
	copy(copied, events)
	return copied, nil
}

// OutboxEnqueue adds an entry to the outbox.
func (s *Store) OutboxEnqueue(ctx context.Context, entry runstore.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry.ID = s.nextID
	s.nextID++
	s.outbox = append(s.outbox, entry)
	return nil
}

// OutboxPoll retrieves unsent outbox entries.
func (s *Store) OutboxPoll(ctx context.Context, limit int) ([]runstore.OutboxEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	unsent := []runstore.OutboxEntry{}
	for _, entry := range s.outbox {
		if !entry.Sent {
			unsent = append(unsent, entry)
			if len(unsent) >= limit {
				break
			}
		}
	}

	return unsent, nil
}

// OutboxMarkSent marks outbox entries as sent.
func (s *Store) OutboxMarkSent(ctx context.Context, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idSet := make(map[int64]bool)
	for _, id := range ids {
		idSet[id] = true
	}

	for i := range s.outbox {
		if idSet[s.outbox[i].ID] {
			s.outbox[i].Sent = true
		}
	}

	return nil
}
