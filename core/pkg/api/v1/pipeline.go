// Package v1 contains public API types for factory core.
package v1

// PipelineSpec defines the complete pipeline configuration.
type PipelineSpec struct {
	Name      string                `json:"name"`
	Resources map[string]Resource   `json:"resources"`
	Stages    []StageSpec           `json:"stages"`
}

// Resource declares a named resource slot with type and access level.
type Resource struct {
	Type   string `json:"type"`   // "git", "http", "s3"
	Access string `json:"access"` // "read-only", "read-write"
}

// StageSpec defines a single stage in the pipeline.
type StageSpec struct {
	Name      string          `json:"name"`
	DependsOn []string        `json:"depends_on,omitempty"`
	Agent     AgentConfig     `json:"agent"`
	Output    OutputConfig    `json:"output"`
	FanIn     *FanInConfig    `json:"fan_in,omitempty"`
	Retry     *RetryConfig    `json:"retry,omitempty"`
}

// AgentConfig specifies how to run the agent.
type AgentConfig struct {
	Image            string              `json:"image"`
	Command          []string            `json:"command"`
	Prompt           string              `json:"prompt"`
	Model            string              `json:"model,omitempty"`
	Credentials      []CredentialBinding `json:"credentials,omitempty"`
	Resources        []string            `json:"resources"` // resource names
	Environment      map[string]string   `json:"environment,omitempty"`
	ChangeDetection  string              `json:"change_detection,omitempty"` // "git", "explicit", "auto" (default: "auto")
}

// CredentialBinding binds a provider credential to the agent.
type CredentialBinding struct {
	Name     string `json:"name"`     // credential name in sandbox env
	Provider string `json:"provider"` // provider name (anthropic, github, etc)
}

// OutputConfig declares expected output type and parameters.
type OutputConfig struct {
	Type         string            `json:"type"` // "pr", "review", "report", "patch", "changeset", "custom"
	Target       string            `json:"target,omitempty"`
	BranchPrefix string            `json:"branch_prefix,omitempty"`
	Schema       map[string]any    `json:"schema,omitempty"`
	Params       map[string]string `json:"params,omitempty"`
}

// FanInConfig specifies how to merge multiple upstream outputs.
type FanInConfig struct {
	Inputs   []string `json:"inputs"` // stage names
	Mode     string   `json:"mode"`   // "deterministic" or "agent"
	Strategy string   `json:"strategy,omitempty"` // "concat", "sequential", "merge_branches", "path_partitioned"
}

// RetryConfig controls retry behavior for a stage.
type RetryConfig struct {
	MaxAttempts           int  `json:"max_attempts"`
	RetryOnTimeout        bool `json:"retry_on_timeout"`
	RetryOnVerification   bool `json:"retry_on_verification"`
}
