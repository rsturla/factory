package v1

import "time"

// CreateRunRequest is the API request to create a pipeline run.
type CreateRunRequest struct {
	PipelineRepo string            `json:"pipeline_repo"` // "github.com/org/pipelines"
	PipelinePath string            `json:"pipeline_path"` // ".factory/cve-triage"
	PipelineRef  string            `json:"pipeline_ref"`  // git ref (branch/tag/SHA)
	Parameters   map[string]string `json:"parameters"`
	Priority     int               `json:"priority"`
}

// PipelineRun represents a single execution of a pipeline.
type PipelineRun struct {
	ID               string            `json:"id"`
	Phase            string            `json:"phase"` // pending/running/succeeded/failed
	PipelineRepo     string            `json:"pipeline_repo"`
	PipelinePath     string            `json:"pipeline_path"`
	PipelineCommit   string            `json:"pipeline_commit"`   // resolved SHA
	PipelineSpec     PipelineSpec      `json:"pipeline_spec"`
	Parameters       map[string]string `json:"parameters"`
	ResourceBindings map[string]string `json:"resource_bindings"` // resource name → resolved URL
	Priority         int               `json:"priority"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	CompletedAt      *time.Time        `json:"completed_at,omitempty"`
}

// StageRun represents a single stage execution within a pipeline run.
type StageRun struct {
	ID              string            `json:"id"`
	RunID           string            `json:"run_id"`
	StageName       string            `json:"stage_name"`
	Phase           string            `json:"phase"` // pending/provisioning_sandbox/running/collecting_output/succeeded/failed
	SandboxID       string            `json:"sandbox_id,omitempty"`
	AgentExecID     string            `json:"agent_exec_id,omitempty"` // provider-specific agent process ID
	AgentConfig     AgentConfig       `json:"agent_config"`
	OutputConfig    OutputConfig      `json:"output_config"`
	Output          map[string]any    `json:"output,omitempty"`
	InitialGitCommit string           `json:"initial_git_commit,omitempty"` // git commit hash before agent starts (for change detection)
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
}

// AuditEvent records an action taken during pipeline execution.
type AuditEvent struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	StageID   string         `json:"stage_id,omitempty"`
	EventType string         `json:"event_type"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}

// ListRunsResponse returns a page of pipeline runs.
type ListRunsResponse struct {
	Runs          []PipelineRun `json:"runs"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

// GetRunResponse returns full run state with all stages.
type GetRunResponse struct {
	Run    PipelineRun `json:"run"`
	Stages []StageRun  `json:"stages"`
}
