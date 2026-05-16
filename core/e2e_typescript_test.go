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
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestEndToEnd_TypeScript verifies TypeScript pipeline execution flow.
func TestEndToEnd_TypeScript(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Mock enqueue client
	mockEnqueue := &mockEnqueueClient{}

	// Create API server pointing to TypeScript example
	apiServer := api.NewServer(store, "examples/simple-ts/.factory", logger)

	// Create orchestrator reconciler
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)

	// Create sandbox manager reconciler
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, sandbox.NewMockProvider(), mockEnqueue, logger)

	// Step 1: Create pipeline run via API (TypeScript pipeline)
	t.Log("Step 1: Creating TypeScript pipeline run via API")
	req := v1.CreateRunRequest{
		PipelineRepo: "github.com/test/test",
		PipelinePath: "test", // Will load examples/simple-ts/.factory/test/pipeline.ts
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
	t.Logf("Created TypeScript run: %s", run.ID)

	// Verify pipeline was loaded from TypeScript
	if run.PipelineSpec.Name != "simple-ts-test" {
		t.Errorf("expected TypeScript pipeline name simple-ts-test, got %s", run.PipelineSpec.Name)
	}

	// Step 2: Verify outbox entry
	outboxEntries, err := store.OutboxPoll(context.Background(), 10)
	if err != nil {
		t.Fatalf("poll outbox: %v", err)
	}
	if len(outboxEntries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(outboxEntries))
	}

	// Mark as sent
	if err := store.OutboxMarkSent(context.Background(), []int64{outboxEntries[0].ID}); err != nil {
		t.Fatalf("mark outbox sent: %v", err)
	}

	// Step 3: Orchestrator reconciles
	t.Log("Step 2: Orchestrator reconciling TypeScript pipeline run")
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

	// Verify stages created
	stages, err := store.ListStages(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list stages: %v", err)
	}
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage from TypeScript pipeline, got %d", len(stages))
	}
	t.Logf("TypeScript pipeline created stage: %s", stages[0].StageName)

	// Step 4: Sandbox manager reconciles stage
	t.Log("Step 3: Sandbox manager reconciling stage")
	stageKey := "stage:" + stages[0].ID

	// Reconcile until completion (mock provider completes instantly)
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

	// Verify stage succeeded
	stage, err := store.GetStage(context.Background(), stages[0].ID)
	if err != nil {
		t.Fatalf("get final stage: %v", err)
	}
	if stage.Phase != "succeeded" {
		t.Errorf("expected stage phase succeeded, got %s", stage.Phase)
	}

	// Step 5: Orchestrator final reconcile
	t.Log("Step 4: Orchestrator final reconcile")
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

	// Verify run succeeded
	finalRun, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get final run: %v", err)
	}
	if finalRun.Phase != "succeeded" {
		t.Errorf("expected run phase succeeded, got %s", finalRun.Phase)
	}

	t.Logf("✓ TypeScript pipeline e2e test passed: run %s completed successfully", run.ID)
}
