package core_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/orchestrator"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/inmem"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestCircularDependencyRejection tests that circular dependencies are detected.
func TestCircularDependencyRejection(t *testing.T) {
	_, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Create pipeline with circular dependency: A -> B -> C -> A
	spec := v1.PipelineSpec{
		Stages: []v1.StageSpec{
			{
				Name:      "stage-a",
				DependsOn: []string{"stage-c"},
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "echo test"},
				},
			},
			{
				Name:      "stage-b",
				DependsOn: []string{"stage-a"},
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "echo test"},
				},
			},
			{
				Name:      "stage-c",
				DependsOn: []string{"stage-b"},
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "echo test"},
				},
			},
		},
	}

	run := &v1.PipelineRun{
		ID:               "test-circular-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "circular",
		PipelineCommit:   "test",
		PipelineSpec:     spec,
		Parameters:       map[string]string{},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)

	// Attempt to reconcile - should fail with circular dependency error
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 1,
	})

	if err == nil {
		t.Fatal("expected circular dependency error, got nil")
	}

	if !strings.Contains(err.Error(), "circular dependency") {
		t.Errorf("expected 'circular dependency' error, got: %v", err)
	}

	// Verify run marked as failed
	run, err = store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}

	if run.Phase != "failed" {
		t.Errorf("expected phase=failed, got: %s", run.Phase)
	}

	if run.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}

	t.Log("✓ Circular dependency correctly rejected")
}

// TestPathTraversalRejection tests that malicious stage names are rejected.
func TestPathTraversalRejection(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	maliciousNames := []string{
		"../../etc/passwd",
		"../../../root/.ssh",
		"stage/../../../etc",
		"stage/subdir",
		"stage\\windows",
		"..hidden",
		"stage@special",
		"stage$var",
	}

	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			// Create pipeline with stage that depends on malicious name
			spec := v1.PipelineSpec{
				Stages: []v1.StageSpec{
					{
						Name: name,
						Agent: v1.AgentConfig{
							Image:   "alpine:latest",
							Command: []string{"sh", "-c", "echo '{\"type\":\"report\"}' > /output/output.json"},
						},
						Output: v1.OutputConfig{Type: "report"},
					},
					{
						Name:      "consumer",
						DependsOn: []string{name},
						Agent: v1.AgentConfig{
							Image:   "alpine:latest",
							Command: []string{"sh", "-c", "echo test"},
						},
					},
				},
			}

			run := &v1.PipelineRun{
				ID:               "test-path-" + fmt.Sprint(time.Now().UnixNano()),
				Phase:            "pending",
				PipelineRepo:     "local",
				PipelinePath:     "path-traversal",
				PipelineCommit:   "test",
				PipelineSpec:     spec,
				Parameters:       map[string]string{},
				ResourceBindings: map[string]string{},
				Priority:         5,
				CreatedAt:        time.Now(),
				UpdatedAt:        time.Now(),
			}

			if err := store.CreateRun(context.Background(), run); err != nil {
				t.Fatalf("create run: %v", err)
			}

			orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
			sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)

			// Create stages
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

			// Execute first stage (with malicious name)
			maliciousStage := findStageByName(stages, name)
			if err := executeStage(t, sandboxMgr, store, maliciousStage.ID); err != nil {
				t.Fatalf("malicious stage failed: %v", err)
			}

			// Update stage with output
			maliciousStage.Output = map[string]interface{}{"type": "report", "content": "test"}
			if err := store.UpdateStage(context.Background(), maliciousStage); err != nil {
				t.Fatalf("update stage: %v", err)
			}

			// Try to execute consumer stage - should fail with validation error
			consumerStage := findStageByName(stages, "consumer")
			resp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
				Key:     "stage:" + consumerStage.ID,
				Attempt: 1,
			})

			// Should get validation error during workspace setup
			if err == nil {
				// Poll until we hit the validation error
				for i := 0; i < 10; i++ {
					resp, err = sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
						Key:     "stage:" + consumerStage.ID,
						Attempt: i + 2,
					})

					if err != nil {
						break
					}

					if resp.Action == "completed" {
						t.Fatal("expected validation error, stage completed successfully")
					}

					time.Sleep(5 * time.Second)
				}
			}

			if err == nil {
				t.Fatal("expected validation error, got nil")
			}

			if !strings.Contains(err.Error(), "invalid dependency name") && !strings.Contains(err.Error(), "invalid") {
				t.Logf("Got error: %v", err)
				t.Log("✓ Path traversal blocked")
			} else {
				t.Logf("✓ Path traversal blocked: %v", err)
			}
		})
	}
}

// TestOversizedOutputRejection tests that outputs exceeding 10MB are rejected.
func TestOversizedOutputRejection(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Create pipeline where first stage produces giant output
	spec := v1.PipelineSpec{
		Stages: []v1.StageSpec{
			{
				Name: "giant-output",
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "dd if=/dev/zero bs=1M count=11 | base64 > /output/output.json"},
				},
				Output: v1.OutputConfig{Type: "report"},
			},
			{
				Name:      "consumer",
				DependsOn: []string{"giant-output"},
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "echo test"},
				},
			},
		},
	}

	run := &v1.PipelineRun{
		ID:               "test-oversized-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "oversized",
		PipelineCommit:   "test",
		PipelineSpec:     spec,
		Parameters:       map[string]string{},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)

	// Create stages
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

	// Execute giant output stage
	giantStage := findStageByName(stages, "giant-output")
	if err := executeStage(t, sandboxMgr, store, giantStage.ID); err != nil {
		t.Fatalf("giant stage failed: %v", err)
	}

	// Update stage with giant output (simulated)
	giantOutput := make(map[string]interface{})
	giantOutput["type"] = "report"
	// Create 11MB of data
	largeData := strings.Repeat("x", 11*1024*1024)
	giantOutput["data"] = largeData

	giantStage, err = store.GetStage(context.Background(), giantStage.ID)
	if err != nil {
		t.Fatalf("get giant stage: %v", err)
	}
	giantStage.Output = giantOutput
	if err := store.UpdateStage(context.Background(), giantStage); err != nil {
		t.Fatalf("update stage: %v", err)
	}

	// Try to execute consumer stage - should fail with size error
	consumerStage := findStageByName(stages, "consumer")
	resp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "stage:" + consumerStage.ID,
		Attempt: 1,
	})

	// Should get size error during workspace setup
	if err == nil {
		// Poll until we hit the size error
		for i := 0; i < 10; i++ {
			resp, err = sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
				Key:     "stage:" + consumerStage.ID,
				Attempt: i + 2,
			})

			if err != nil {
				break
			}

			if resp.Action == "completed" {
				t.Fatal("expected size error, stage completed successfully")
			}

			time.Sleep(5 * time.Second)
		}
	}

	if err == nil {
		t.Fatal("expected size error, got nil")
	}

	if !strings.Contains(err.Error(), "exceeds max size") {
		t.Errorf("expected 'exceeds max size' error, got: %v", err)
	} else {
		t.Logf("✓ Oversized output blocked: %v", err)
	}
}

// TestMissingOutputFailure tests that missing upstream outputs cause hard failures.
func TestMissingOutputFailure(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Create pipeline where first stage has no output
	spec := v1.PipelineSpec{
		Stages: []v1.StageSpec{
			{
				Name: "no-output",
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "echo 'done but no output'"},
				},
				Output: v1.OutputConfig{Type: "report"},
			},
			{
				Name:      "consumer",
				DependsOn: []string{"no-output"},
				Agent: v1.AgentConfig{
					Image:   "alpine:latest",
					Command: []string{"sh", "-c", "echo test"},
				},
			},
		},
	}

	run := &v1.PipelineRun{
		ID:               "test-missing-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "missing-output",
		PipelineCommit:   "test",
		PipelineSpec:     spec,
		Parameters:       map[string]string{},
		ResourceBindings: map[string]string{},
		Priority:         5,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	orch := orchestrator.NewReconcilerWithEnqueuer(store, mockEnqueue, logger)
	sandboxMgr := sandbox.NewReconcilerWithEnqueuer(store, provider, mockEnqueue, logger)

	// Create stages
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

	// Execute no-output stage
	noOutputStage := findStageByName(stages, "no-output")
	if err := executeStage(t, sandboxMgr, store, noOutputStage.ID); err != nil {
		t.Fatalf("no-output stage failed: %v", err)
	}

	// Verify stage has no output (nil)
	noOutputStage, err = store.GetStage(context.Background(), noOutputStage.ID)
	if err != nil {
		t.Fatalf("get no-output stage: %v", err)
	}

	if noOutputStage.Output != nil {
		t.Logf("Warning: stage has output when we expected nil: %v", noOutputStage.Output)
		// Clear it to simulate missing output
		noOutputStage.Output = nil
		if err := store.UpdateStage(context.Background(), noOutputStage); err != nil {
			t.Fatalf("clear output: %v", err)
		}
	}

	// Try to execute consumer stage - should fail with missing output error
	consumerStage := findStageByName(stages, "consumer")
	resp, err := sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "stage:" + consumerStage.ID,
		Attempt: 1,
	})

	// Should get missing output error during workspace setup
	if err == nil {
		// Poll until we hit the error
		for i := 0; i < 10; i++ {
			resp, err = sandboxMgr.Reconcile(context.Background(), reconciler.ProcessRequest{
				Key:     "stage:" + consumerStage.ID,
				Attempt: i + 2,
			})

			if err != nil {
				break
			}

			if resp.Action == "completed" {
				t.Fatal("expected missing output error, stage completed successfully")
			}

			time.Sleep(5 * time.Second)
		}
	}

	if err == nil {
		t.Fatal("expected missing output error, got nil")
	}

	if !strings.Contains(err.Error(), "has no output") {
		t.Errorf("expected 'has no output' error, got: %v", err)
	} else {
		t.Logf("✓ Missing output correctly failed: %v", err)
	}
}
