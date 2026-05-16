package output

import (
	"context"
	"fmt"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// Handler processes stage output according to its type.
type Handler interface {
	// Validate checks if the raw output conforms to expected format.
	Validate(ctx context.Context, output map[string]any) error

	// Execute performs the output action (create PR, post review, etc).
	Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error)
}

// ExecuteParams contains context for output execution.
type ExecuteParams struct {
	StageRun *v1.StageRun
	Output   map[string]any
	RunID    string
}

// ExecuteResult contains the result of output execution.
type ExecuteResult struct {
	Success bool
	Message string
	Details map[string]any
}

// Registry manages output handlers by type.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry creates an empty handler registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]Handler),
	}
}

// Register adds a handler for a given output type.
func (r *Registry) Register(outputType string, handler Handler) {
	r.handlers[outputType] = handler
}

// Get retrieves the handler for an output type.
func (r *Registry) Get(outputType string) (Handler, error) {
	handler, ok := r.handlers[outputType]
	if !ok {
		return nil, fmt.Errorf("no handler for output type: %s", outputType)
	}
	return handler, nil
}

// DefaultRegistry returns a registry with built-in handlers.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register("report", &ReportHandler{})
	r.Register("pr", &PRHandler{})
	// TODO: Add more handlers: review, patch, changeset
	return r
}
