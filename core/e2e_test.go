package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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

// mockEnqueueClient tracks enqueued items for test verification.
type mockEnqueueClient struct {
	enqueued []mockEnqueueItem
}

type mockEnqueueItem struct {
	queue    string
	key      string
	priority int
}

func (m *mockEnqueueClient) Enqueue(ctx context.Context, queue, key string, priority int) error {
	m.enqueued = append(m.enqueued, mockEnqueueItem{queue: queue, key: key, priority: priority})
	return nil
}

// TestEndToEnd verifies the complete pipeline execution flow:
// API creates run → orchestrator creates stages → sandbox manager executes → run completes
func TestEndToEnd(t *testing.T) {
	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	// Mock enqueue client to track cross-queue fan-out
	mockEnqueue := &mockEnqueueClient{}

	// Create API server
	apiServer := api.NewServer(store, "examples/simple-test/.factory", logger)

	// Create orchestrator reconciler
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)

	// Create sandbox manager reconciler with mock provider
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, sandbox.NewMockProvider(), mockEnqueue, logger)

	// Step 1: Create pipeline run via API
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

	if w.Code != http.StatusCreated {
		t.Fatalf("create run failed: %d %s", w.Code, w.Body.String())
	}

	var run v1.PipelineRun
	if err := json.NewDecoder(w.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	t.Logf("Created run: %s", run.ID)

	// Verify outbox entry created
	outboxEntries, err := store.OutboxPoll(context.Background(), 10)
	if err != nil {
		t.Fatalf("poll outbox: %v", err)
	}
	if len(outboxEntries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(outboxEntries))
	}
	if outboxEntries[0].Queue != "sf-pipeline" {
		t.Errorf("expected queue sf-pipeline, got %s", outboxEntries[0].Queue)
	}

	// Step 2: Simulate outbox poller enqueuing to sf-pipeline
	// (In real system, outbox poller would enqueue via EnqueueClient)
	t.Log("Step 2: Simulating outbox→sf-pipeline enqueue")
	if err := store.OutboxMarkSent(context.Background(), []int64{outboxEntries[0].ID}); err != nil {
		t.Fatalf("mark outbox sent: %v", err)
	}

	// Step 3: Orchestrator reconciles pipeline run
	t.Log("Step 3: Orchestrator reconciling pipeline run")
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
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].StageName != "test" {
		t.Errorf("expected stage name 'test', got %s", stages[0].StageName)
	}
	if stages[0].Phase != "pending" {
		t.Errorf("expected stage phase pending, got %s", stages[0].Phase)
	}
	t.Logf("Created stage: %s (phase: %s)", stages[0].ID, stages[0].Phase)

	// Verify orchestrator enqueued stage to sf-stage
	if len(mockEnqueue.enqueued) != 1 {
		t.Fatalf("expected 1 enqueued item, got %d", len(mockEnqueue.enqueued))
	}
	if mockEnqueue.enqueued[0].queue != "sf-stage" {
		t.Errorf("expected queue sf-stage, got %s", mockEnqueue.enqueued[0].queue)
	}

	// Step 4: Sandbox manager reconciles stage (multiple times for state machine)
	t.Log("Step 4: Sandbox manager reconciling stage")
	stageKey := "stage:" + stages[0].ID

	// First reconcile: provision sandbox
	sandboxResp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     stageKey,
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("sandbox reconcile 1: %v", err)
	}
	if sandboxResp.Action != reconciler.ActionRequeue {
		t.Errorf("expected requeue, got %s", sandboxResp.Action)
	}

	// Refresh stage state
	stage, err := store.GetStage(context.Background(), stages[0].ID)
	if err != nil {
		t.Fatalf("get stage: %v", err)
	}
	t.Logf("Stage phase after provision: %s", stage.Phase)

	// Keep reconciling until stage completes (mock provider completes immediately)
	maxIterations := 10
	for i := 0; i < maxIterations; i++ {
		stage, err = store.GetStage(context.Background(), stages[0].ID)
		if err != nil {
			t.Fatalf("get stage iteration %d: %v", i, err)
		}

		if stage.Phase == "succeeded" || stage.Phase == "failed" {
			break
		}

		sandboxResp, err = sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
			Key:     stageKey,
			Attempt: i + 2,
		})
		if err != nil {
			t.Fatalf("sandbox reconcile %d: %v", i+2, err)
		}

		t.Logf("Iteration %d: phase=%s action=%s", i+1, stage.Phase, sandboxResp.Action)

		if sandboxResp.Action == reconciler.ActionCompleted {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	// Verify stage succeeded
	stage, err = store.GetStage(context.Background(), stages[0].ID)
	if err != nil {
		t.Fatalf("get final stage: %v", err)
	}
	if stage.Phase != "succeeded" {
		t.Errorf("expected stage phase succeeded, got %s", stage.Phase)
	}
	if stage.Output == nil {
		t.Error("expected stage output to be set")
	}
	t.Logf("Stage completed: %s (output collected: %v)", stage.ID, stage.Output != nil)

	// Step 5: Orchestrator final reconcile to mark run complete
	t.Log("Step 5: Orchestrator final reconcile")
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
	if finalRun.CompletedAt == nil {
		t.Error("expected run completed_at to be set")
	}

	t.Logf("✓ End-to-end test passed: run %s completed successfully", run.ID)
}
