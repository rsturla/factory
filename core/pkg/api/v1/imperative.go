package v1

import "time"

// ImperativeCreateRunRequest creates a run without pipeline spec.
// Used by imperative SDK — stages added via CreateStageRequest.
type ImperativeCreateRunRequest struct {
	Name string `json:"name,omitempty"`
}

// ImperativeCreateStageRequest creates a stage within an existing run.
type ImperativeCreateStageRequest struct {
	Name            string                 `json:"name"`
	Image           string                 `json:"image"`
	Command         []string               `json:"command"`
	Prompt          string                 `json:"prompt,omitempty"`
	Model           string                 `json:"model,omitempty"`
	Credentials     []CredentialBinding    `json:"credentials,omitempty"`
	Resources       []ResourceBinding      `json:"resources,omitempty"`
	Environment     map[string]string      `json:"environment,omitempty"`
	Timeout         string                 `json:"timeout,omitempty"`
	Retry           int                    `json:"retry,omitempty"`
	Inputs          map[string]interface{} `json:"inputs,omitempty"`
	Output          *OutputAction          `json:"output,omitempty"`
	ChangeDetection string                 `json:"change_detection,omitempty"` // "git", "explicit", "auto"
}

// ResourceBinding for imperative stages.
type ResourceBinding struct {
	Type        string `json:"type"` // git|http|s3
	Access      string `json:"access,omitempty"`
	URL         string `json:"url,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Description string `json:"description,omitempty"`
}

// OutputAction for imperative stages.
type OutputAction struct {
	Type      string   `json:"type"` // pr|review|report|patch|changeset
	Branch    string   `json:"branch,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Reviewers []string `json:"reviewers,omitempty"`
	Draft     bool     `json:"draft,omitempty"`
}

// ImperativeStageResponse returned after creating stage.
type ImperativeStageResponse struct {
	ID        string            `json:"id"`
	RunID     string            `json:"run_id"`
	Name      string            `json:"name"`
	Status    string            `json:"status"` // pending|running|completed|failed
	Output    map[string]any    `json:"output,omitempty"`
	ExitCode  int               `json:"exit_code,omitempty"`
	Duration  float64           `json:"duration,omitempty"` // seconds
	Logs      string            `json:"logs,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ImperativeRunResponse returned after creating run.
type ImperativeRunResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
