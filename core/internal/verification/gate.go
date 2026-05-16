package verification

import (
	"context"
	"fmt"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// Gate validates output before handler execution.
type Gate interface {
	// Check validates the stage output and returns error if check fails.
	Check(ctx context.Context, stage *v1.StageRun) error

	// Name returns the gate name for logging.
	Name() string
}

// Runner executes multiple gates in sequence.
type Runner struct {
	gates []Gate
}

// NewRunner creates a verification gate runner.
func NewRunner(gates []Gate) *Runner {
	return &Runner{gates: gates}
}

// Run executes all gates in order, stopping on first failure.
func (r *Runner) Run(ctx context.Context, stage *v1.StageRun) error {
	for _, gate := range r.gates {
		if err := gate.Check(ctx, stage); err != nil {
			return fmt.Errorf("gate %s failed: %w", gate.Name(), err)
		}
	}
	return nil
}

// DefaultRunner returns a runner with standard gates.
func DefaultRunner() *Runner {
	return NewRunner([]Gate{
		&NoSecretsGate{},
		&DiffSizeGate{MaxLines: 5000, MaxFiles: 100},
		&PathAllowedGate{
			DenyPatterns: []string{
				"*.env",
				"credentials.*",
				".git/config",
				".ssh/*",
				"**/secrets/*",
			},
		},
	})
}
