package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/orchestrator"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/pipeline"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestRealWorkload_ParallelCodeReview tests a realistic parallel code review pipeline.
func TestRealWorkload_ParallelCodeReview(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Load parallel review pipeline
	loader := pipeline.NewLoader("examples/code-review/.factory")
	spec, err := loader.Load("parallel-review")
	if err != nil {
		t.Fatalf("load pipeline: %v", err)
	}

	// Verify pipeline structure
	if len(spec.Stages) != 4 {
		t.Fatalf("expected 4 stages, got %d", len(spec.Stages))
	}

	// Check parallel stages have no dependencies
	parallelStages := []string{"security-review", "performance-review", "style-review"}
	for _, name := range parallelStages {
		found := false
		for _, stage := range spec.Stages {
			if stage.Name == name {
				found = true
				if len(stage.DependsOn) != 0 {
					t.Errorf("parallel stage %s should have no dependencies, got: %v", name, stage.DependsOn)
				}
			}
		}
		if !found {
			t.Errorf("missing parallel stage: %s", name)
		}
	}

	// Check synthesize stage depends on all 3 parallel stages
	var synthesizeStage *v1.StageSpec
	for i := range spec.Stages {
		if spec.Stages[i].Name == "synthesize" {
			synthesizeStage = &spec.Stages[i]
			break
		}
	}
	if synthesizeStage == nil {
		t.Fatal("synthesize stage not found")
	}
	if len(synthesizeStage.DependsOn) != 3 {
		t.Errorf("synthesize stage should depend on 3 stages, got: %v", synthesizeStage.DependsOn)
	}

	t.Log("✓ Pipeline structure validated")

	// Create run
	run := &v1.PipelineRun{
		ID:               "review-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "parallel-review",
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

	t.Logf("Created code review run: %s", run.ID)

	// Setup
	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)

	// PHASE 1: Create stages
	t.Log("=== PHASE 1: Creating stages ===")
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

	if len(stages) != 4 {
		t.Fatalf("expected 4 stages, got %d", len(stages))
	}

	// Verify 3 parallel stages were enqueued
	if len(mockEnqueue.enqueued) != 3 {
		t.Fatalf("expected 3 parallel stages enqueued, got %d", len(mockEnqueue.enqueued))
	}

	t.Logf("✓ Created 4 stages, enqueued 3 parallel stages")

	// PHASE 2: Execute parallel review stages
	t.Log("=== PHASE 2: Executing parallel reviews ===")

	securityStage := findStageByName(stages, "security-review")
	performanceStage := findStageByName(stages, "performance-review")
	styleStage := findStageByName(stages, "style-review")

	// Copy sample code into each sandbox before execution
	// We'll do this by creating a custom provider wrapper, but for simplicity
	// we'll just mount the code directory

	// Execute all 3 in parallel using goroutines
	type stageResult struct {
		name     string
		duration time.Duration
		err      error
	}

	results := make(chan stageResult, 3)
	startTime := time.Now()

	executeWithCodeMount := func(stageID, stageName string) {
		start := time.Now()

		// Get current working directory
		cwd, err := os.Getwd()
		if err != nil {
			results <- stageResult{stageName, 0, fmt.Errorf("get cwd: %w", err)}
			return
		}

		codePath := filepath.Join(cwd, "examples/code-review")

		// Custom execution with volume mount
		stage, err := store.GetStage(context.Background(), stageID)
		if err != nil {
			results <- stageResult{stageName, 0, err}
			return
		}

		// Provision sandbox
		_, err = sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
			Key:     "stage:" + stageID,
			Attempt: 1,
		})
		if err != nil {
			results <- stageResult{stageName, 0, err}
			return
		}

		// Wait for provisioning
		time.Sleep(5 * time.Second)

		// Copy code directory into sandbox
		stage, err = store.GetStage(context.Background(), stageID)
		if err != nil {
			results <- stageResult{stageName, 0, err}
			return
		}

		// Create /workspace/code directory
		if _, err := provider.Exec(context.Background(), stage.SandboxID, []string{"mkdir", "-p", "/workspace/code/sample-code"}, nil); err != nil {
			results <- stageResult{stageName, 0, fmt.Errorf("mkdir: %w", err)}
			return
		}

		// Read sample code file
		apiCode, err := os.ReadFile(filepath.Join(codePath, "sample-code", "api.js"))
		if err != nil {
			results <- stageResult{stageName, 0, fmt.Errorf("read api.js: %w", err)}
			return
		}

		// Copy code file into sandbox
		if err := provider.CopyTo(context.Background(), stage.SandboxID, "/workspace/code/sample-code/api.js", apiCode); err != nil {
			results <- stageResult{stageName, 0, fmt.Errorf("copy code: %w", err)}
			return
		}

		// Continue execution
		for i := 0; i < 10; i++ {
			resp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
				Key:     "stage:" + stageID,
				Attempt: i + 2,
			})
			if err != nil {
				results <- stageResult{stageName, 0, err}
				return
			}

			if resp.Action == "completed" {
				break
			}

			time.Sleep(5 * time.Second)
		}

		// Verify completed
		stage, err = store.GetStage(context.Background(), stageID)
		if err != nil {
			results <- stageResult{stageName, 0, err}
			return
		}

		if stage.Phase != "succeeded" {
			results <- stageResult{stageName, 0, fmt.Errorf("stage failed: phase=%s", stage.Phase)}
			return
		}

		duration := time.Since(start)
		results <- stageResult{stageName, duration, nil}
	}

	// Launch parallel executions
	go executeWithCodeMount(securityStage.ID, "security-review")
	go executeWithCodeMount(performanceStage.ID, "performance-review")
	go executeWithCodeMount(styleStage.ID, "style-review")

	// Collect results
	var maxDuration time.Duration
	for i := 0; i < 3; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("%s failed: %v", result.name, result.err)
		}
		t.Logf("✓ %s completed in %v", result.name, result.duration)
		if result.duration > maxDuration {
			maxDuration = result.duration
		}
	}

	parallelTime := time.Since(startTime)
	t.Logf("✓ All 3 parallel reviews completed in %v (longest individual: %v)", parallelTime, maxDuration)

	// Validate outputs
	securityStage, _ = store.GetStage(context.Background(), securityStage.ID)
	performanceStage, _ = store.GetStage(context.Background(), performanceStage.ID)
	styleStage, _ = store.GetStage(context.Background(), styleStage.ID)

	// Check security findings
	if securityStage.Output == nil {
		t.Error("security review has no output")
	} else {
		secFindings := int(securityStage.Output["findings"].(float64))
		if secFindings < 3 {
			t.Errorf("expected >=3 security findings, got %d", secFindings)
		}
		t.Logf("✓ Security review found %d issues", secFindings)
	}

	// Check performance findings
	if performanceStage.Output == nil {
		t.Error("performance review has no output")
	} else {
		perfFindings := int(performanceStage.Output["findings"].(float64))
		if perfFindings < 2 {
			t.Errorf("expected >=2 performance findings, got %d", perfFindings)
		}
		t.Logf("✓ Performance review found %d issues", perfFindings)
	}

	// Check style findings
	if styleStage.Output == nil {
		t.Error("style review has no output")
	} else {
		styleFindings := int(styleStage.Output["findings"].(float64))
		if styleFindings < 3 {
			t.Errorf("expected >=3 style findings, got %d", styleFindings)
		}
		t.Logf("✓ Style review found %d issues", styleFindings)
	}

	// PHASE 3: Orchestrator should now enqueue synthesize stage
	t.Log("=== PHASE 3: Enqueuing synthesis ===")
	mockEnqueue.enqueued = nil
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 2: %v", err)
	}

	if len(mockEnqueue.enqueued) != 1 {
		t.Fatalf("expected synthesize stage enqueued, got %d", len(mockEnqueue.enqueued))
	}

	t.Logf("✓ Synthesize stage enqueued after dependencies completed")

	// PHASE 4: Execute synthesis stage
	t.Log("=== PHASE 4: Executing synthesis with upstream inputs ===")

	synthesizeStageRun := findStageByName(stages, "synthesize")

	// Execute synthesis (no need to mount code, it reads upstream outputs)
	if err := executeStage(t, sandboxMgr, store, synthesizeStageRun.ID); err != nil {
		t.Fatalf("synthesize stage failed: %v", err)
	}

	synthesizeStageRun, _ = store.GetStage(context.Background(), synthesizeStageRun.ID)

	if synthesizeStageRun.Phase != "succeeded" {
		t.Errorf("expected synthesize succeeded, got %s", synthesizeStageRun.Phase)
	}

	// Validate synthesis output
	if synthesizeStageRun.Output == nil {
		t.Fatal("synthesize has no output")
	}

	synthesisJSON, _ := json.MarshalIndent(synthesizeStageRun.Output, "", "  ")
	t.Logf("Synthesis output:\n%s", synthesisJSON)

	// Check synthesis received all upstream reviews
	upstreamReviews, ok := synthesizeStageRun.Output["upstream_reviews"]
	if !ok {
		t.Error("synthesis missing upstream_reviews field")
	} else {
		reviews := upstreamReviews.([]interface{})
		if len(reviews) != 3 {
			t.Errorf("expected 3 upstream reviews, got %d", len(reviews))
		}
		t.Logf("✓ Synthesis received %d upstream reviews", len(reviews))
	}

	// Check total findings
	totalFindings, ok := synthesizeStageRun.Output["total_findings"]
	if !ok {
		t.Error("synthesis missing total_findings")
	} else {
		total := int(totalFindings.(float64))
		if total < 8 {
			t.Errorf("expected >=8 total findings, got %d", total)
		}
		t.Logf("✓ Total findings: %d", total)
	}

	// Check status
	status, ok := synthesizeStageRun.Output["status"]
	if !ok {
		t.Error("synthesis missing status")
	} else {
		if status != "NEEDS_WORK" {
			t.Errorf("expected status=NEEDS_WORK (due to security issues), got: %s", status)
		}
		t.Logf("✓ Status: %s", status)
	}

	// PHASE 5: Final orchestrator reconcile - mark run succeeded
	t.Log("=== PHASE 5: Completing pipeline ===")
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 3,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 3: %v", err)
	}

	run, _ = store.GetRun(context.Background(), run.ID)
	if run.Phase != "succeeded" {
		t.Errorf("expected run succeeded, got: %s", run.Phase)
	}

	t.Logf("✓ Pipeline completed: %s", run.Phase)

	// Summary
	t.Log("=== REAL WORKLOAD TEST SUMMARY ===")
	t.Logf("✓ Parallel execution: 3 reviews in %v", parallelTime)
	t.Logf("✓ Security issues: %d", int(securityStage.Output["findings"].(float64)))
	t.Logf("✓ Performance issues: %d", int(performanceStage.Output["findings"].(float64)))
	t.Logf("✓ Style issues: %d", int(styleStage.Output["findings"].(float64)))
	t.Logf("✓ Total issues: %d", int(synthesizeStageRun.Output["total_findings"].(float64)))
	t.Logf("✓ Final status: %s", synthesizeStageRun.Output["status"])
	t.Log("✓ PARALLEL CODE REVIEW PIPELINE WORKING")
}
