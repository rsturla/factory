// Package sandbox handles sandbox lifecycle and execution.
package sandbox

import (
	"context"
	"time"
)

// SandboxProvider abstracts sandbox creation and management.
// Implementations: mock (tests), docker (local dev), openshell (production).
type SandboxProvider interface {
	// Create provisions a new sandbox.
	Create(ctx context.Context, spec SandboxSpec) (*SandboxHandle, error)

	// Get retrieves current sandbox status.
	Get(ctx context.Context, id string) (*SandboxStatus, error)

	// Exec runs a command inside the sandbox.
	// Returns stdout, stderr, exit code.
	Exec(ctx context.Context, id string, cmd []string, stdin []byte) (*ExecResult, error)

	// ExecDetached starts a command in background.
	// Returns process ID for status polling.
	ExecDetached(ctx context.Context, id string, cmd []string) (string, error)

	// ExecStatus checks if a detached process is still running.
	// Returns running status and exit code (if finished).
	ExecStatus(ctx context.Context, id string, execID string) (*ExecStatusResult, error)

	// CopyFrom extracts a file/directory from the sandbox.
	CopyFrom(ctx context.Context, id string, path string) ([]byte, error)

	// CopyTo writes a file to the sandbox.
	CopyTo(ctx context.Context, id string, path string, data []byte) error

	// Delete tears down the sandbox.
	Delete(ctx context.Context, id string) error
}

// SandboxSpec describes how to provision a sandbox.
type SandboxSpec struct {
	ID          string            // unique sandbox ID
	Image       string            // OCI image
	Environment map[string]string // env vars
	Resources   ResourceRequirements
	GPU         bool              // request GPU
	GPUDevice   string            // specific GPU (optional)
}

// ResourceRequirements specifies compute resources.
type ResourceRequirements struct {
	CPURequest    string // e.g. "500m", "2"
	CPULimit      string
	MemoryRequest string // e.g. "256Mi", "4Gi"
	MemoryLimit   string
}

// SandboxHandle is returned after sandbox creation.
type SandboxHandle struct {
	ID     string
	Status SandboxPhase
}

// SandboxStatus describes current sandbox state.
type SandboxStatus struct {
	ID         string
	Phase      SandboxPhase
	Ready      bool
	Message    string
	InstanceID string // platform-specific instance ID
	CreatedAt  time.Time
}

// SandboxPhase represents sandbox lifecycle state.
type SandboxPhase string

const (
	SandboxPhaseProvisioning SandboxPhase = "provisioning"
	SandboxPhaseReady        SandboxPhase = "ready"
	SandboxPhaseError        SandboxPhase = "error"
	SandboxPhaseDeleting     SandboxPhase = "deleting"
)

// ExecResult contains command execution output.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ExecStatusResult contains detached process status.
type ExecStatusResult struct {
	Running  bool
	ExitCode int // only valid if Running == false
}
