package prompt

import (
	"context"
	"fmt"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
)

// DeliveryMode defines how prompts are delivered to agents.
type DeliveryMode string

const (
	// DeliveryFile writes prompt to /workspace/.prompt.md
	DeliveryFile DeliveryMode = "file"
	// Future: DeliveryStdin, DeliveryEnv, DeliveryArg
)

// Deliverer writes rendered prompts to sandbox.
type Deliverer struct {
	provider sandbox.SandboxProvider
	mode     DeliveryMode
}

// NewDeliverer creates a prompt deliverer.
func NewDeliverer(provider sandbox.SandboxProvider, mode DeliveryMode) *Deliverer {
	if mode == "" {
		mode = DeliveryFile
	}
	return &Deliverer{
		provider: provider,
		mode:     mode,
	}
}

// Deliver writes the prompt to the sandbox according to delivery mode.
func (d *Deliverer) Deliver(ctx context.Context, sandboxID string, prompt string) error {
	switch d.mode {
	case DeliveryFile:
		return d.deliverFile(ctx, sandboxID, prompt)
	default:
		return fmt.Errorf("unsupported delivery mode: %s", d.mode)
	}
}

// deliverFile writes prompt to /workspace/.prompt.md
func (d *Deliverer) deliverFile(ctx context.Context, sandboxID string, prompt string) error {
	// Write to /workspace/.prompt.md
	if err := d.provider.CopyTo(ctx, sandboxID, "/workspace/.prompt.md", []byte(prompt)); err != nil {
		return fmt.Errorf("write prompt file: %w", err)
	}
	return nil
}
