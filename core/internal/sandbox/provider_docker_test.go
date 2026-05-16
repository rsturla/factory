package sandbox

import (
	"context"
	"testing"
	"time"
)

func TestDockerProvider_ExecDetached(t *testing.T) {
	provider, err := NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	ctx := context.Background()

	// Create sandbox
	spec := SandboxSpec{
		ID:    "test-exec-detached",
		Image: "alpine:latest",
	}

	_, err = provider.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer provider.Delete(ctx, spec.ID)

	// Wait for container ready
	for i := 0; i < 30; i++ {
		status, err := provider.Get(ctx, spec.ID)
		if err != nil {
			t.Fatalf("get status: %v", err)
		}
		if status.Ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Start background process (sleep 2 seconds)
	execID, err := provider.ExecDetached(ctx, spec.ID, []string{"sleep", "2"})
	if err != nil {
		t.Fatalf("exec detached: %v", err)
	}

	if execID == "" {
		t.Fatal("exec ID should not be empty")
	}

	// Check status immediately - should be running
	status, err := provider.ExecStatus(ctx, spec.ID, execID)
	if err != nil {
		t.Fatalf("check status: %v", err)
	}

	if !status.Running {
		t.Error("expected process to be running initially")
	}

	// Wait for completion
	time.Sleep(3 * time.Second)

	// Check status again - should be finished
	status, err = provider.ExecStatus(ctx, spec.ID, execID)
	if err != nil {
		t.Fatalf("check status after completion: %v", err)
	}

	if status.Running {
		t.Error("expected process to have finished")
	}

	if status.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", status.ExitCode)
	}
}

func TestDockerProvider_ExecDetached_NonZeroExit(t *testing.T) {
	provider, err := NewDockerProvider()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}

	ctx := context.Background()

	// Create sandbox
	spec := SandboxSpec{
		ID:    "test-exec-exit-code",
		Image: "alpine:latest",
	}

	_, err = provider.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer provider.Delete(ctx, spec.ID)

	// Wait for container ready
	for i := 0; i < 30; i++ {
		status, err := provider.Get(ctx, spec.ID)
		if err != nil {
			t.Fatalf("get status: %v", err)
		}
		if status.Ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Start command that fails (exit 42)
	execID, err := provider.ExecDetached(ctx, spec.ID, []string{"sh", "-c", "exit 42"})
	if err != nil {
		t.Fatalf("exec detached: %v", err)
	}

	// Wait for completion
	time.Sleep(1 * time.Second)

	// Check exit code
	status, err := provider.ExecStatus(ctx, spec.ID, execID)
	if err != nil {
		t.Fatalf("check status: %v", err)
	}

	if status.Running {
		t.Error("expected process to have finished")
	}

	if status.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", status.ExitCode)
	}
}
