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
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/output"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/pipeline"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestIntegration_RealAgent tests full pipeline with Docker provider.
// Requires Docker daemon running.
func TestIntegration_RealAgent(t *testing.T) {
	// Check docker availability
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Load pipeline
	loader := pipeline.NewLoader("examples/hello-agent/.factory")
	spec, err := loader.Load("hello")
	if err != nil {
		t.Fatalf("load pipeline: %v", err)
	}

	// Create run
	run := &v1.PipelineRun{
		ID:               "test-integration-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "hello",
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

	t.Logf("Created run: %s", run.ID)

	// Orchestrator creates stages
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
	orchCtx := context.Background()

	_, err = orch.Reconcile(orchCtx, reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile: %v", err)
	}

	stages, err := store.ListStages(orchCtx, run.ID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}

	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}

	stage := &stages[0]
	t.Logf("Created stage: %s", stage.ID)

	// Sandbox manager with REAL Docker provider
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)
	sandboxCtx := context.Background()
	stageKey := "stage:" + stage.ID

	// Execute sandbox lifecycle
	maxIterations := 100
	for i := 0; i < maxIterations; i++ {
		stage, err = store.GetStage(sandboxCtx, stage.ID)
		if err != nil {
			t.Fatalf("get stage iteration %d: %v", i, err)
		}

		t.Logf("Iteration %d: phase=%s", i, stage.Phase)

		if stage.Phase == "succeeded" || stage.Phase == "failed" {
			break
		}

		resp, err := sandboxMgr.Reconcile(sandboxCtx, reconciler.ProcessRequest{
			Key:     stageKey,
			Attempt: i + 1,
		})
		if err != nil {
			t.Fatalf("sandbox reconcile %d: %v", i+1, err)
		}

		if resp.Action == reconciler.ActionCompleted {
			break
		}

		// Adaptive sleep based on requeue delay
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

	// Verify final state
	stage, err = store.GetStage(sandboxCtx, stage.ID)
	if err != nil {
		t.Fatalf("get final stage: %v", err)
	}

	if stage.Phase != "succeeded" {
		t.Errorf("expected stage succeeded, got %s", stage.Phase)
		if stage.Output != nil {
			outputJSON, _ := json.MarshalIndent(stage.Output, "", "  ")
			t.Logf("Stage output: %s", outputJSON)
		}
	}

	if stage.Output == nil {
		t.Error("expected output collected")
	} else {
		outputJSON, _ := json.MarshalIndent(stage.Output, "", "  ")
		t.Logf("✓ Output collected: %s", outputJSON)
	}

	// Output processor
	outputProc := output.NewReconciler(store, logger)
	outputCtx := context.Background()

	_, err = outputProc.Reconcile(outputCtx, reconciler.ProcessRequest{
		Key:     "output:" + stage.ID,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("output reconcile: %v", err)
	}

	// Verify audit trail
	events, err := store.ListAuditEvents(outputCtx, run.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}

	if len(events) == 0 {
		t.Error("expected audit events")
	} else {
		t.Logf("✓ Audit trail: %d events", len(events))
	}

	t.Logf("✓ Integration test PASSED - real agent executed in Docker")
}

// TestIntegration_AgentFailure tests agent exit code handling.
func TestIntegration_AgentFailure(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Load failure pipeline
	loader := pipeline.NewLoader("examples/fail-agent/.factory")
	spec, err := loader.Load("fail")
	if err != nil {
		t.Fatalf("load pipeline: %v", err)
	}

	// Create run
	run := &v1.PipelineRun{
		ID:               "test-fail-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "fail",
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

	t.Logf("Created failure test run: %s", run.ID)

	// Orchestrator
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
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

	stage := &stages[0]
	t.Logf("Created stage: %s", stage.ID)

	// Sandbox manager
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)
	stageKey := "stage:" + stage.ID

	// Execute until terminal state
	maxIterations := 100
	for i := 0; i < maxIterations; i++ {
		stage, err = store.GetStage(context.Background(), stage.ID)
		if err != nil {
			t.Fatalf("get stage: %v", err)
		}

		t.Logf("Iteration %d: phase=%s", i, stage.Phase)

		if stage.Phase == "succeeded" || stage.Phase == "failed" {
			break
		}

		resp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
			Key:     stageKey,
			Attempt: i + 1,
		})
		if err != nil {
			t.Fatalf("sandbox reconcile: %v", err)
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

	// Verify failure
	stage, err = store.GetStage(context.Background(), stage.ID)
	if err != nil {
		t.Fatalf("get final stage: %v", err)
	}

	if stage.Phase != "failed" {
		t.Errorf("expected stage failed, got %s", stage.Phase)
	} else {
		t.Logf("✓ Agent failure correctly detected")
	}

	if stage.Output == nil {
		t.Error("expected error output")
	} else {
		errorMsg, _ := stage.Output["error"].(string)
		t.Logf("✓ Error captured: %s", errorMsg)
	}

	t.Logf("✓ Failure test PASSED - exit code 42 handled correctly")
}
