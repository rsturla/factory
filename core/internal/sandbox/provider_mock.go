package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockProvider is an in-memory sandbox provider for tests.
type MockProvider struct {
	mu        sync.RWMutex
	sandboxes map[string]*mockSandbox
	execs     map[string]*mockExec
}

type mockSandbox struct {
	spec      SandboxSpec
	phase     SandboxPhase
	ready     bool
	files     map[string][]byte
	createdAt time.Time
}

type mockExec struct {
	startedAt time.Time
	duration  time.Duration // how long process runs
	exitCode  int
}

// NewMockProvider creates a mock sandbox provider.
func NewMockProvider() *MockProvider {
	return &MockProvider{
		sandboxes: make(map[string]*mockSandbox),
		execs:     make(map[string]*mockExec),
	}
}

// Create provisions a mock sandbox (immediately ready).
func (m *MockProvider) Create(ctx context.Context, spec SandboxSpec) (*SandboxHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sandboxes[spec.ID]; exists {
		return nil, fmt.Errorf("sandbox already exists: %s", spec.ID)
	}

	sb := &mockSandbox{
		spec:      spec,
		phase:     SandboxPhaseReady,
		ready:     true,
		files:     make(map[string][]byte),
		createdAt: time.Now(),
	}
	m.sandboxes[spec.ID] = sb

	return &SandboxHandle{
		ID:     spec.ID,
		Status: SandboxPhaseReady,
	}, nil
}

// Get retrieves sandbox status.
func (m *MockProvider) Get(ctx context.Context, id string) (*SandboxStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sb, ok := m.sandboxes[id]
	if !ok {
		return nil, fmt.Errorf("sandbox not found: %s", id)
	}

	return &SandboxStatus{
		ID:         id,
		Phase:      sb.phase,
		Ready:      sb.ready,
		Message:    "mock sandbox",
		InstanceID: id,
		CreatedAt:  sb.createdAt,
	}, nil
}

// Exec simulates command execution (always exits 0).
func (m *MockProvider) Exec(ctx context.Context, id string, cmd []string, stdin []byte) (*ExecResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.sandboxes[id]; !ok {
		return nil, fmt.Errorf("sandbox not found: %s", id)
	}

	// Mock: echo command to stdout
	stdout := []byte(fmt.Sprintf("mock exec: %v\n", cmd))
	return &ExecResult{
		Stdout:   stdout,
		Stderr:   nil,
		ExitCode: 0,
	}, nil
}

// ExecDetached starts a mock background process.
// Process completes instantly (for tests) or after duration if set.
func (m *MockProvider) ExecDetached(ctx context.Context, id string, cmd []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sandboxes[id]; !ok {
		return "", fmt.Errorf("sandbox not found: %s", id)
	}

	execID := fmt.Sprintf("exec-%s-%d", id, time.Now().UnixNano())
	m.execs[execID] = &mockExec{
		startedAt: time.Now(),
		duration:  0, // instant completion for tests
		exitCode:  0,
	}

	return execID, nil
}

// ExecStatus checks mock process status.
func (m *MockProvider) ExecStatus(ctx context.Context, id string, execID string) (*ExecStatusResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	exec, ok := m.execs[execID]
	if !ok {
		return nil, fmt.Errorf("exec not found: %s", execID)
	}

	elapsed := time.Since(exec.startedAt)
	running := elapsed < exec.duration

	return &ExecStatusResult{
		Running:  running,
		ExitCode: exec.exitCode,
	}, nil
}

// CopyFrom reads a file from the mock sandbox.
func (m *MockProvider) CopyFrom(ctx context.Context, id string, path string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sb, ok := m.sandboxes[id]
	if !ok {
		return nil, fmt.Errorf("sandbox not found: %s", id)
	}

	data, ok := sb.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}

	return data, nil
}

// CopyTo writes a file to the mock sandbox.
func (m *MockProvider) CopyTo(ctx context.Context, id string, path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sb, ok := m.sandboxes[id]
	if !ok {
		return fmt.Errorf("sandbox not found: %s", id)
	}

	sb.files[path] = data
	return nil
}

// Delete removes the mock sandbox.
func (m *MockProvider) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sandboxes[id]; !ok {
		return fmt.Errorf("sandbox not found: %s", id)
	}

	delete(m.sandboxes, id)
	return nil
}
