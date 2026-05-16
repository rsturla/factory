package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/api"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/orchestrator"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/output"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestEndToEnd_OutputProcessing verifies full pipeline flow with output processing.
func TestEndToEnd_OutputProcessing(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	mockEnqueue := &mockEnqueueClient{}

	// Create API server
	apiServer := api.NewServer(store, "examples/simple-test/.factory", logger)

	// Create orchestrator
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)

	// Create sandbox manager
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, sandbox.NewMockProvider(), mockEnqueue, logger)

	// Create output processor
	outputProc := output.NewReconciler(store, logger)

	// Step 1: Create pipeline run
	t.Log("Step 1: Creating pipeline run via API")
	req := v1.CreateRunRequest{
		PipelineRepo: "github.com/test/test",
		PipelinePath: "test",
		PipelineRef:  "main",
		Parameters: map[string]string{
			"resource.test-repo.url": "github.com/test/repo",
		},
		Priority: 5,
	}

	body, _ := json.Marshal(req)
	httpReq := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
	w := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(w, httpReq)

	if w.Code != 201 {
		t.Fatalf("create run failed: %d %s", w.Code, w.Body.String())
	}

	var run v1.PipelineRun
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	t.Logf("Created run: %s", run.ID)

	// Step 2: Orchestrator creates stages
	t.Log("Step 2: Orchestrator reconciling")
	orchResp, err := orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile: %v", err)
	}
	if orchResp.Action != reconciler.ActionRequeue {
		t.Errorf("expected requeue, got %s", orchResp.Action)
	}

	stages, err := store.ListStages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	t.Logf("Created stage: %s", stages[0].ID)

	// Step 3: Sandbox manager runs agent
	t.Log("Step 3: Sandbox manager reconciling stage")
	stageKey := "stage:" + stages[0].ID

	maxIterations := 10
	for i := 0; i < maxIterations; i++ {
		stage, err := store.GetStage(context.Background(), stages[0].ID)
		if err != nil {
			t.Fatalf("get stage iteration %d: %v", i, err)
		}

		if stage.Phase == "succeeded" || stage.Phase == "failed" {
			break
		}

		sandboxResp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
			Key:     stageKey,
			Attempt: i + 1,
		})
		if err != nil {
			t.Fatalf("sandbox reconcile %d: %v", i+1, err)
		}

		if sandboxResp.Action == reconciler.ActionCompleted {
			break
		}

		time.Sleep(50 * time.Millisecond)
	}

	stage, err := store.GetStage(context.Background(), stages[0].ID)
	if err != nil {
		t.Fatalf("get final stage: %v", err)
	}
	if stage.Phase != "succeeded" {
		t.Errorf("expected stage phase succeeded, got %s", stage.Phase)
	}
	if stage.Output == nil {
		t.Error("expected stage to have output")
	}
	t.Logf("Stage completed with output: %v", stage.Output)

	// Step 4: Output processor validates and executes
	t.Log("Step 4: Output processor reconciling")
	outputKey := "output:" + stage.ID

	outputResp, err := outputProc.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     outputKey,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("output reconcile: %v", err)
	}
	if outputResp.Action != reconciler.ActionCompleted {
		t.Errorf("expected completed, got %s", outputResp.Action)
	}

	// Step 5: Verify audit trail
	t.Log("Step 5: Verifying audit trail")
	auditEvents, err := store.ListAuditEvents(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}

	foundOutputEvent := false
	for _, event := range auditEvents {
		if event.EventType == "output_processed" {
			foundOutputEvent = true
			if event.StageID != stage.ID {
				t.Errorf("audit event stage_id mismatch: %s vs %s", event.StageID, stage.ID)
			}
			t.Logf("Audit event recorded: %s", event.EventType)
			break
		}
	}

	if !foundOutputEvent {
		t.Error("expected output_processed audit event")
	}

	// Step 6: Orchestrator final reconcile
	t.Log("Step 6: Orchestrator final reconcile")
	orchResp, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("orchestrator final reconcile: %v", err)
	}
	if orchResp.Action != reconciler.ActionCompleted {
		t.Errorf("expected completed, got %s", orchResp.Action)
	}

	finalRun, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get final run: %v", err)
	}
	if finalRun.Phase != "succeeded" {
		t.Errorf("expected run phase succeeded, got %s", finalRun.Phase)
	}

	t.Logf("✓ Full pipeline with output processing completed: run %s", run.ID)
}

// TestEndToEnd_OutputValidation verifies output validation failures.
func TestEndToEnd_OutputValidation(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	outputProc := output.NewReconciler(store, logger)

	// Create a stage with oversized output
	stage := &v1.StageRun{
		ID:      "stage-oversized",
		RunID:   "run-test",
		Phase:   "succeeded",
		OutputConfig: v1.OutputConfig{
			Type: "report",
		},
		Output: map[string]any{
			"content": string(make([]byte, 60*1024*1024)), // 60MB > 50MB limit
		},
	}

	if err := store.CreateStage(context.Background(), stage); err != nil {
		t.Fatalf("create stage: %v", err)
	}

	// Process output
	outputResp, err := outputProc.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "output:stage-oversized",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("output reconcile: %v", err)
	}
	if outputResp.Action != reconciler.ActionCompleted {
		t.Errorf("expected completed, got %s", outputResp.Action)
	}

	// Verify stage marked as failed
	failedStage, err := store.GetStage(context.Background(), "stage-oversized")
	if err != nil {
		t.Fatalf("get stage: %v", err)
	}
	if failedStage.Phase != "failed" {
		t.Errorf("expected stage phase failed, got %s", failedStage.Phase)
	}
	if failedStage.Output["error"] == nil {
		t.Error("expected error in output")
	}

	t.Logf("✓ Output validation correctly rejected oversized output")
}
