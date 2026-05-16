package core_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/orchestrator"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/pipeline"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestParallelExecution tests multi-stage pipeline with parallel execution and fan-in.
func TestParallelExecution(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Load parallel pipeline
	loader := pipeline.NewLoader("examples/parallel-stages/.factory")
	spec, err := loader.Load("parallel")
	if err != nil {
		t.Fatalf("load pipeline: %v", err)
	}

	if len(spec.Stages) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(spec.Stages))
	}

	// Verify dependencies
	if len(spec.Stages[2].DependsOn) != 2 {
		t.Errorf("expected synthesize to depend on 2 stages, got %d", len(spec.Stages[2].DependsOn))
	}

	// Create run
	run := &v1.PipelineRun{
		ID:               "test-parallel-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "parallel",
		PipelineCommit:   "test",
		PipelineSpec:     *spec,
		Parameters:       map[string]string{},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	t.Logf("Created parallel pipeline run: %s", run.ID)

	// Orchestrator
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
	orchCtx := context.Background()

	// First reconcile - should create all 3 stages and enqueue 2 parallel stages
	t.Log("=== Orchestrator reconcile 1: Create stages ===")
	_, err = orch.Reconcile(orchCtx, reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 1: %v", err)
	}

	stages, err := store.ListStages(orchCtx, run.ID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}

	if len(stages) != 3 {
		t.Fatalf("expected 3 stages created, got %d", len(stages))
	}

	// Verify parallel stages enqueued (review-security, review-performance)
	if len(mockEnqueue.enqueued) != 2 {
		t.Fatalf("expected 2 stages enqueued in parallel, got %d", len(mockEnqueue.enqueued))
	}

	t.Logf("✓ Parallel stages enqueued: %s, %s",
		mockEnqueue.enqueued[0].key, mockEnqueue.enqueued[1].key)

	// Sandbox manager
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)

	// Execute both parallel stages
	stage1 := findStageByName(stages, "review-security")
	stage2 := findStageByName(stages, "review-performance")

	if stage1 == nil || stage2 == nil {
		t.Fatal("parallel stages not found")
	}

	t.Log("=== Executing parallel stages ===")
	startTime := time.Now()

	// Run stages concurrently
	errChan := make(chan error, 2)
	go func() {
		errChan <- executeStage(t, sandboxMgr, store, stage1.ID)
	}()
	go func() {
		errChan <- executeStage(t, sandboxMgr, store, stage2.ID)
	}()

	// Wait for both
	for i := 0; i < 2; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("parallel stage execution failed: %v", err)
		}
	}

	elapsed := time.Since(startTime)
	t.Logf("✓ Both parallel stages completed in %v", elapsed)

	// Verify both succeeded
	stage1, err = store.GetStage(orchCtx, stage1.ID)
	if err != nil {
		t.Fatalf("get stage1: %v", err)
	}
	stage2, err = store.GetStage(orchCtx, stage2.ID)
	if err != nil {
		t.Fatalf("get stage2: %v", err)
	}

	if stage1.Phase != "succeeded" || stage2.Phase != "succeeded" {
		t.Errorf("expected both stages succeeded, got %s and %s", stage1.Phase, stage2.Phase)
	}

	t.Logf("✓ Stage 1 output: %v", stage1.Output)
	t.Logf("✓ Stage 2 output: %v", stage2.Output)

	// Orchestrator reconcile 2 - should enqueue synthesize stage now that deps are met
	t.Log("=== Orchestrator reconcile 2: Enqueue fan-in stage ===")
	mockEnqueue.enqueued = nil // clear
	_, err = orch.Reconcile(orchCtx, reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 2: %v", err)
	}

	// Should enqueue synthesize stage
	if len(mockEnqueue.enqueued) != 1 {
		t.Fatalf("expected 1 fan-in stage enqueued, got %d", len(mockEnqueue.enqueued))
	}

	t.Logf("✓ Fan-in stage enqueued: %s", mockEnqueue.enqueued[0].key)

	// Execute fan-in stage
	stage3 := findStageByName(stages, "synthesize")
	if stage3 == nil {
		t.Fatal("fan-in stage not found")
	}

	t.Log("=== Executing fan-in stage ===")
	if err := executeStage(t, sandboxMgr, store, stage3.ID); err != nil {
		t.Fatalf("fan-in stage execution: %v", err)
	}

	stage3, err = store.GetStage(orchCtx, stage3.ID)
	if err != nil {
		t.Fatalf("get stage3: %v", err)
	}

	if stage3.Phase != "succeeded" {
		t.Errorf("expected fan-in stage succeeded, got %s", stage3.Phase)
	}

	t.Logf("✓ Fan-in stage output: %v", stage3.Output)

	// Final orchestrator reconcile - should mark run as succeeded
	t.Log("=== Orchestrator reconcile 3: Mark run succeeded ===")
	_, err = orch.Reconcile(orchCtx, reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 3,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 3: %v", err)
	}

	run, err = store.GetRun(orchCtx, run.ID)
	if err != nil {
		t.Fatalf("get final run: %v", err)
	}

	if run.Phase != "succeeded" {
		t.Errorf("expected run succeeded, got %s", run.Phase)
	}

	t.Logf("✓ PARALLEL EXECUTION TEST PASSED")
	t.Logf("  - 2 stages ran in parallel")
	t.Logf("  - 1 fan-in stage waited for dependencies")
	t.Logf("  - Total time: %v", elapsed)
}

// executeStage runs a stage through its full lifecycle.
func executeStage(t *testing.T, sandboxMgr *sandbox.Reconciler, store *inmem.Store, stageID string) error {
	stageKey := "stage:" + stageID

	maxIterations := 100
	for i := 0; i < maxIterations; i++ {
		stage, err := store.GetStage(context.Background(), stageID)
		if err != nil {
			return fmt.Errorf("get stage: %w", err)
		}

		if stage.Phase == "succeeded" || stage.Phase == "failed" {
			if stage.Phase == "failed" {
				return fmt.Errorf("stage failed: %v", stage.Output)
			}
			return nil
		}

		resp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
			Key:     stageKey,
			Attempt: i + 1,
		})
		if err != nil {
			return fmt.Errorf("sandbox reconcile: %w", err)
		}

		if resp.Action == reconciler.ActionCompleted {
			break
		}

		if resp.RequeueAfter != "" {
			delay, _ := time.ParseDuration(resp.RequeueAfter)
			if delay > 0 {
				time.Sleep(delay)
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return nil
}

// findStageByName finds a stage in the list by name.
func findStageByName(stages []v1.StageRun, name string) *v1.StageRun {
	for i := range stages {
		if stages[i].StageName == name {
			return &stages[i]
		}
	}
	return nil
}
