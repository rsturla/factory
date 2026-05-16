package v1

// OutputHandler processes agent output and executes the corresponding action.
type OutputHandler interface {
	// Validate checks that the raw output matches expected format.
	Validate(raw []byte) error

	// Execute performs the output action (create PR, post review, etc).
	Execute(params ExecuteParams) (*ExecuteResult, error)
}

// ExecuteParams contains context for output execution.
type ExecuteParams struct {
	RunID       string
	StageID     string
	OutputType  string
	OutputData  map[string]any
	Config      OutputConfig
	Credentials map[string]string
}

// ExecuteResult contains the outcome of output execution.
type ExecuteResult struct {
	Success bool              `json:"success"`
	Message string            `json:"message"`
	URL     string            `json:"url,omitempty"`     // PR URL, report URL, etc
	Metadata map[string]string `json:"metadata,omitempty"`
}
