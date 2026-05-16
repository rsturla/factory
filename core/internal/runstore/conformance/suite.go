// Package conformance provides a shared test suite for runstore implementations.
// Both postgres and inmem backends must pass these tests.
package conformance

import (
	"context"
	"testing"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// TestSuite runs all conformance tests against a Store implementation.
func TestSuite(t *testing.T, newStore func(t *testing.T) runstore.Store) {
	t.Run("PipelineRun", func(t *testing.T) {
		testPipelineRunCRUD(t, newStore(t))
	})
	t.Run("StageRun", func(t *testing.T) {
		testStageRunCRUD(t, newStore(t))
	})
	t.Run("AuditEvents", func(t *testing.T) {
		testAuditEvents(t, newStore(t))
	})
	t.Run("Outbox", func(t *testing.T) {
		testOutbox(t, newStore(t))
	})
}

func testPipelineRunCRUD(t *testing.T, store runstore.Store) {
	ctx := context.Background()

	run := &v1.PipelineRun{
		ID:             "run-1",
		Phase:          "pending",
		PipelineRepo:   "github.com/org/pipelines",
		PipelinePath:   ".factory/test",
		PipelineCommit: "abc123",
		PipelineSpec: v1.PipelineSpec{
			Name:      "test-pipeline",
			Resources: map[string]v1.Resource{},
			Stages:    []v1.StageSpec{},
		},
		Parameters:       map[string]string{"key": "value"},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	// Create
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Get
	fetched, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if fetched.Phase != "pending" {
		t.Errorf("expected phase pending, got %s", fetched.Phase)
	}

	// Update
	completed := time.Now().UTC()
	run.Phase = "succeeded"
	run.UpdatedAt = completed
	run.CompletedAt = &completed
	if err := store.UpdateRun(ctx, run); err != nil {
		t.Fatalf("UpdateRun failed: %v", err)
	}

	// Verify update
	fetched, err = store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after update failed: %v", err)
	}
	if fetched.Phase != "succeeded" {
		t.Errorf("expected phase succeeded, got %s", fetched.Phase)
	}
	if fetched.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}

	// List
	runs, _, err := store.ListRuns(ctx, runstore.ListRunsFilters{Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(runs))
	}

	// List with phase filter
	runs, _, err = store.ListRuns(ctx, runstore.ListRunsFilters{Phase: "succeeded", Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns with filter failed: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 succeeded run, got %d", len(runs))
	}

	runs, _, err = store.ListRuns(ctx, runstore.ListRunsFilters{Phase: "pending", Limit: 10})
	if err != nil {
		t.Fatalf("ListRuns with filter failed: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 pending runs, got %d", len(runs))
	}
}

func testStageRunCRUD(t *testing.T, store runstore.Store) {
	ctx := context.Background()

	// Create parent run first
	run := &v1.PipelineRun{
		ID:               "run-2",
		Phase:            "running",
		PipelineRepo:     "github.com/org/pipelines",
		PipelinePath:     ".factory/test",
		PipelineCommit:   "abc123",
		PipelineSpec:     v1.PipelineSpec{Name: "test", Resources: map[string]v1.Resource{}, Stages: []v1.StageSpec{}},
		Parameters:       map[string]string{},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	stage := &v1.StageRun{
		ID:        "stage-1",
		RunID:     "run-2",
		StageName: "build",
		Phase:     "pending",
		AgentConfig: v1.AgentConfig{
			Image:   "test-image",
			Command: []string{"test"},
		},
	}

	// Create
	if err := store.CreateStage(ctx, stage); err != nil {
		t.Fatalf("CreateStage failed: %v", err)
	}

	// Get
	fetched, err := store.GetStage(ctx, stage.ID)
	if err != nil {
		t.Fatalf("GetStage failed: %v", err)
	}
	if fetched.Phase != "pending" {
		t.Errorf("expected phase pending, got %s", fetched.Phase)
	}

	// Update
	started := time.Now().UTC()
	stage.Phase = "running"
	stage.SandboxID = "sandbox-1"
	stage.StartedAt = &started
	if err := store.UpdateStage(ctx, stage); err != nil {
		t.Fatalf("UpdateStage failed: %v", err)
	}

	// Verify update
	fetched, err = store.GetStage(ctx, stage.ID)
	if err != nil {
		t.Fatalf("GetStage after update failed: %v", err)
	}
	if fetched.Phase != "running" {
		t.Errorf("expected phase running, got %s", fetched.Phase)
	}
	if fetched.SandboxID != "sandbox-1" {
		t.Errorf("expected sandbox_id sandbox-1, got %s", fetched.SandboxID)
	}

	// List
	stages, err := store.ListStages(ctx, "run-2")
	if err != nil {
		t.Fatalf("ListStages failed: %v", err)
	}
	if len(stages) != 1 {
		t.Errorf("expected 1 stage, got %d", len(stages))
	}
}

func testAuditEvents(t *testing.T, store runstore.Store) {
	ctx := context.Background()

	// Create parent run
	run := &v1.PipelineRun{
		ID:               "run-3",
		Phase:            "running",
		PipelineRepo:     "github.com/org/pipelines",
		PipelinePath:     ".factory/test",
		PipelineCommit:   "abc123",
		PipelineSpec:     v1.PipelineSpec{Name: "test", Resources: map[string]v1.Resource{}, Stages: []v1.StageSpec{}},
		Parameters:       map[string]string{},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	event := &v1.AuditEvent{
		ID:        "event-1",
		RunID:     "run-3",
		EventType: "run.created",
		Detail:    map[string]any{"action": "create"},
		CreatedAt: time.Now().UTC(),
	}

	// Create
	if err := store.CreateAuditEvent(ctx, event); err != nil {
		t.Fatalf("CreateAuditEvent failed: %v", err)
	}

	// List
	events, err := store.ListAuditEvents(ctx, "run-3")
	if err != nil {
		t.Fatalf("ListAuditEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != "run.created" {
		t.Errorf("expected event type run.created, got %s", events[0].EventType)
	}
}

func testOutbox(t *testing.T, store runstore.Store) {
	ctx := context.Background()

	entry := runstore.OutboxEntry{
		Queue:     "sf-pipeline",
		Key:       "run:123",
		Priority:  5,
		Sent:      false,
		CreatedAt: time.Now().UTC(),
	}

	// Enqueue
	if err := store.OutboxEnqueue(ctx, entry); err != nil {
		t.Fatalf("OutboxEnqueue failed: %v", err)
	}

	// Poll
	entries, err := store.OutboxPoll(ctx, 10)
	if err != nil {
		t.Fatalf("OutboxPoll failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Queue != "sf-pipeline" {
		t.Errorf("expected queue sf-pipeline, got %s", entries[0].Queue)
	}

	// Mark sent
	ids := []int64{entries[0].ID}
	if err := store.OutboxMarkSent(ctx, ids); err != nil {
		t.Fatalf("OutboxMarkSent failed: %v", err)
	}

	// Poll again - should be empty
	entries, err = store.OutboxPoll(ctx, 10)
	if err != nil {
		t.Fatalf("OutboxPoll after mark sent failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after mark sent, got %d", len(entries))
	}
}
