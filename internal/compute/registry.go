package compute

import (
	"context"
	"encoding/json"
	"fmt"
)

// ProviderFactory creates a Provider from a JSON config blob.
type ProviderFactory func(ctx context.Context, configJSON json.RawMessage) (Provider, error)

// Registry maps compute backend names to their factory functions.
type Registry struct {
	factories map[string]ProviderFactory
}

// NewRegistry creates a registry with the noop provider pre-registered.
func NewRegistry() *Registry {
	r := &Registry{
		factories: make(map[string]ProviderFactory),
	}
	r.Register("noop", func(_ context.Context, _ json.RawMessage) (Provider, error) {
		return NoopProvider{}, nil
	})
	return r
}

// Register adds a provider factory to the registry.
func (r *Registry) Register(name string, factory ProviderFactory) {
	r.factories[name] = factory
}

// Create instantiates a Provider by name, passing optional JSON config.
func (r *Registry) Create(ctx context.Context, name string, configJSON json.RawMessage) (Provider, error) {
	factory, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("unknown compute backend: %q", name)
	}
	return factory(ctx, configJSON)
}
