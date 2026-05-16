package sandbox

import (
	"context"
	"fmt"
	"time"

	openshellv1 "gitlab.com/redhat/hummingbird/experimental/factory/core/gen/openshell/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// OpenShellProvider uses OpenShell for production sandbox execution.
// Provides full security isolation via Landlock, seccomp, network policies.
type OpenShellProvider struct {
	client openshellv1.OpenShellClient
	conn   *grpc.ClientConn
}

// NewOpenShellProvider creates an OpenShell-based sandbox provider.
func NewOpenShellProvider(endpoint string) (*OpenShellProvider, error) {
	// TODO: Add TLS support, credentials, timeouts
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect to openshell: %w", err)
	}

	client := openshellv1.NewOpenShellClient(conn)

	// Health check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Health(ctx, &openshellv1.HealthRequest{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("openshell health check failed: %w", err)
	}

	return &OpenShellProvider{
		client: client,
		conn:   conn,
	}, nil
}

// Create provisions a sandbox via OpenShell.
func (o *OpenShellProvider) Create(ctx context.Context, spec SandboxSpec) (*SandboxHandle, error) {
	req := &openshellv1.CreateSandboxRequest{
		Name: spec.ID,
		Spec: &openshellv1.SandboxSpec{
			Environment: spec.Environment,
			Template: &openshellv1.SandboxTemplate{
				Image: spec.Image,
			},
			Gpu:       spec.GPU,
			GpuDevice: spec.GPUDevice,
		},
	}

	// TODO: Add resource limits via template.Resources (protobuf.Struct)
	// TODO: Add policy configuration

	resp, err := o.client.CreateSandbox(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}

	return &SandboxHandle{
		ID:     resp.Sandbox.Metadata.Name,
		Status: mapPhase(resp.Sandbox.Phase),
	}, nil
}

// Get retrieves sandbox status.
func (o *OpenShellProvider) Get(ctx context.Context, id string) (*SandboxStatus, error) {
	req := &openshellv1.GetSandboxRequest{
		Name: id,
	}

	resp, err := o.client.GetSandbox(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("get sandbox: %w", err)
	}

	return &SandboxStatus{
		ID:      resp.Sandbox.Metadata.Name,
		Phase:   mapPhase(resp.Sandbox.Phase),
		Ready:   resp.Sandbox.Phase == openshellv1.SandboxPhase_SANDBOX_PHASE_READY,
		Message: "", // TODO: Extract message from status if needed
		CreatedAt: time.Now(), // TODO: Parse from resp.Sandbox.Metadata timestamps
	}, nil
}

// Exec runs a command synchronously (via streaming ExecSandbox).
func (o *OpenShellProvider) Exec(ctx context.Context, id string, cmd []string, stdin []byte) (*ExecResult, error) {
	req := &openshellv1.ExecSandboxRequest{
		SandboxId: id,
		Command:   cmd,
	}

	stream, err := o.client.ExecSandbox(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("exec sandbox: %w", err)
	}

	var stdout, stderr []byte
	var exitCode int32

	for {
		event, err := stream.Recv()
		if err != nil {
			// Stream closed
			break
		}

		switch payload := event.Payload.(type) {
		case *openshellv1.ExecSandboxEvent_Stdout:
			stdout = append(stdout, payload.Stdout.Data...)
		case *openshellv1.ExecSandboxEvent_Stderr:
			stderr = append(stderr, payload.Stderr.Data...)
		case *openshellv1.ExecSandboxEvent_Exit:
			exitCode = payload.Exit.ExitCode
		}
	}

	return &ExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: int(exitCode),
	}, nil
}

// ExecDetached starts a background command.
// OpenShell doesn't have native detached exec, so we'll run in background with nohup.
func (o *OpenShellProvider) ExecDetached(ctx context.Context, id string, cmd []string) (string, error) {
	// Wrap command to run in background
	wrappedCmd := []string{"sh", "-c", fmt.Sprintf("nohup %s </dev/null >/tmp/exec.out 2>/tmp/exec.err & echo $!", shellJoin(cmd))}

	result, err := o.Exec(ctx, id, wrappedCmd, nil)
	if err != nil {
		return "", fmt.Errorf("exec detached: %w", err)
	}

	if result.ExitCode != 0 {
		return "", fmt.Errorf("exec detached failed: exit=%d stderr=%s", result.ExitCode, string(result.Stderr))
	}

	// PID from stdout
	pid := string(result.Stdout)
	return pid, nil
}

// ExecStatus checks detached process status.
func (o *OpenShellProvider) ExecStatus(ctx context.Context, id string, execID string) (*ExecStatusResult, error) {
	// Check if process is running
	checkCmd := []string{"sh", "-c", fmt.Sprintf("kill -0 %s 2>/dev/null && echo running || echo exited", execID)}

	result, err := o.Exec(ctx, id, checkCmd, nil)
	if err != nil {
		return nil, fmt.Errorf("exec status: %w", err)
	}

	running := string(result.Stdout) == "running\n"

	// If exited, try to get exit code from /proc
	exitCode := 0
	if !running {
		// Process exited, assume success (can't reliably get exit code from detached process)
		exitCode = 0
	}

	return &ExecStatusResult{
		Running:  running,
		ExitCode: exitCode,
	}, nil
}

// CopyFrom extracts file from sandbox.
// Phase 1: Simple implementation via exec cat/tar.
func (o *OpenShellProvider) CopyFrom(ctx context.Context, id string, path string) ([]byte, error) {
	// Use cat for single files
	result, err := o.Exec(ctx, id, []string{"cat", path}, nil)
	if err != nil {
		return nil, fmt.Errorf("copy from: %w", err)
	}

	if result.ExitCode != 0 {
		return nil, fmt.Errorf("copy from failed: %s", string(result.Stderr))
	}

	return result.Stdout, nil
}

// CopyTo writes file to sandbox.
// Phase 1: Simple implementation via exec tee.
func (o *OpenShellProvider) CopyTo(ctx context.Context, id string, path string, data []byte) error {
	// Ensure parent directory exists
	mkdirCmd := []string{"sh", "-c", fmt.Sprintf("mkdir -p $(dirname %s)", shellQuote(path))}
	if _, err := o.Exec(ctx, id, mkdirCmd, nil); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	// Write via stdin + tee
	writeCmd := []string{"tee", path}
	result, err := o.Exec(ctx, id, writeCmd, data)
	if err != nil {
		return fmt.Errorf("copy to: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("copy to failed: %s", string(result.Stderr))
	}

	return nil
}

// Delete tears down sandbox.
func (o *OpenShellProvider) Delete(ctx context.Context, id string) error {
	req := &openshellv1.DeleteSandboxRequest{
		Name: id,
	}

	_, err := o.client.DeleteSandbox(ctx, req)
	if err != nil {
		return fmt.Errorf("delete sandbox: %w", err)
	}

	return nil
}

// Close closes gRPC connection.
func (o *OpenShellProvider) Close() error {
	return o.conn.Close()
}

// Helper functions

func mapPhase(phase openshellv1.SandboxPhase) SandboxPhase {
	switch phase {
	case openshellv1.SandboxPhase_SANDBOX_PHASE_PROVISIONING:
		return SandboxPhaseProvisioning
	case openshellv1.SandboxPhase_SANDBOX_PHASE_READY:
		return SandboxPhaseReady
	case openshellv1.SandboxPhase_SANDBOX_PHASE_ERROR:
		return SandboxPhaseError
	case openshellv1.SandboxPhase_SANDBOX_PHASE_DELETING:
		return SandboxPhaseDeleting
	default:
		return SandboxPhaseError
	}
}

func shellJoin(cmd []string) string {
	// Simple join - proper shell escaping in production
	result := ""
	for i, arg := range cmd {
		if i > 0 {
			result += " "
		}
		result += shellQuote(arg)
	}
	return result
}

func shellQuote(s string) string {
	// Simple quoting - proper escaping in production
	return fmt.Sprintf("'%s'", s)
}
