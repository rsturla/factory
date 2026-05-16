package core_test

import (
	"context"
	"encoding/json"
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

// TestFanIn tests cross-stage artifact passing.
func TestFanIn(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Load fanin pipeline
	loader := pipeline.NewLoader("examples/fanin-test/.factory")
	spec, err := loader.Load("fanin")
	if err != nil {
		t.Fatalf("load pipeline: %v", err)
	}

	// Create run
	run := &v1.PipelineRun{
		ID:               "test-fanin-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "fanin",
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

	t.Logf("Created fan-in test run: %s", run.ID)

	// Orchestrator
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)

	// Create stages
	t.Log("=== Creating stages ===")
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile: %v", err)
	}

	stages, err := store.ListStages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}

	// Execute stage1 and stage2
	stage1 := findStageByName(stages, "stage1")
	stage2 := findStageByName(stages, "stage2")

	t.Log("=== Executing upstream stages ===")
	if err := executeStage(t, sandboxMgr, store, stage1.ID); err != nil {
		t.Fatalf("stage1 failed: %v", err)
	}
	if err := executeStage(t, sandboxMgr, store, stage2.ID); err != nil {
		t.Fatalf("stage2 failed: %v", err)
	}

	// Verify outputs
	stage1, err = store.GetStage(context.Background(), stage1.ID)
	if err != nil {
		t.Fatalf("get stage1: %v", err)
	}
	stage2, err = store.GetStage(context.Background(), stage2.ID)
	if err != nil {
		t.Fatalf("get stage2: %v", err)
	}

	t.Logf("✓ Stage1 output: %v", stage1.Output)
	t.Logf("✓ Stage2 output: %v", stage2.Output)

	// Orchestrator should now enqueue merge stage
	t.Log("=== Enqueuing fan-in stage ===")
	mockEnqueue.enqueued = nil
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 2: %v", err)
	}

	if len(mockEnqueue.enqueued) != 1 {
		t.Fatalf("expected merge stage enqueued, got %d", len(mockEnqueue.enqueued))
	}

	// Execute merge stage - should have upstream outputs available
	mergeStage := findStageByName(stages, "merge")
	t.Log("=== Executing fan-in stage with upstream inputs ===")

	if err := executeStage(t, sandboxMgr, store, mergeStage.ID); err != nil {
		t.Fatalf("merge stage failed: %v", err)
	}

	mergeStage, err = store.GetStage(context.Background(), mergeStage.ID)
	if err != nil {
		t.Fatalf("get merge stage: %v", err)
	}

	if mergeStage.Phase != "succeeded" {
		t.Errorf("expected merge stage succeeded, got %s", mergeStage.Phase)
		if mergeStage.Output != nil {
			outputJSON, _ := json.MarshalIndent(mergeStage.Output, "", "  ")
			t.Logf("Output: %s", outputJSON)
		}
	}

	t.Logf("✓ Merge stage output: %v", mergeStage.Output)

	// Verify merge stage received upstream outputs
	upstreamFiles, ok := mergeStage.Output["upstream_files"]
	if !ok {
		t.Error("merge stage did not report upstream_files")
	} else {
		t.Logf("✓ Merge stage had access to upstream outputs: %v", upstreamFiles)
	}

	t.Logf("✓ FAN-IN TEST PASSED - Cross-stage artifact passing working")
}
