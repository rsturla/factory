// Package compute defines the interface for managing worker lifecycle
// on different compute backends (Kubernetes, EC2, etc.).
package compute

import "context"

// Provider manages worker lifecycle on a specific compute backend.
type Provider interface {
	// Name returns the backend identifier (e.g., "kubernetes", "ec2").
	Name() string

	// EnsureWorkers ensures the desired number of workers exist for a queue.
	EnsureWorkers(ctx context.Context, queue string, desired int) error

	// ScaleToZero shuts down all workers for a queue.
	ScaleToZero(ctx context.Context, queue string) error

	// WorkerStatus returns the status of workers from the backend's perspective.
	WorkerStatus(ctx context.Context, queue string) ([]WorkerInfo, error)

	// Cleanup removes workers that are no longer registered.
	Cleanup(ctx context.Context, queue string) error
}

// WorkerInfo describes a worker from the compute backend's perspective.
type WorkerInfo struct {
	ID       string
	Backend  string
	Status   string // "running", "pending", "terminating"
	Metadata map[string]string
}

// NoopProvider is a Provider that does nothing.
// Used when compute scaling is handled externally (e.g., by HPA).
type NoopProvider struct{}

func (NoopProvider) Name() string                                              { return "noop" }
func (NoopProvider) EnsureWorkers(context.Context, string, int) error          { return nil }
func (NoopProvider) ScaleToZero(context.Context, string) error                 { return nil }
func (NoopProvider) WorkerStatus(context.Context, string) ([]WorkerInfo, error) { return nil, nil }
func (NoopProvider) Cleanup(context.Context, string) error                     { return nil }
