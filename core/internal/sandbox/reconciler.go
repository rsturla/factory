// Package sandbox implements sandbox lifecycle reconciler.
// Pattern: Delegated Polling - requeues per step with adaptive timing.
package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/artifact"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/gitproxy"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// Enqueuer enqueues work into workqueue queues.
type Enqueuer interface {
	Enqueue(ctx context.Context, queue, key string, priority int) error
}

// Reconciler manages sandbox lifecycle for stage execution.
type Reconciler struct {
	store         runstore.Store
	provider      SandboxProvider
	enqueue       Enqueuer
	tokenMinter   *gitproxy.TokenMinter
	gitProxyURL   string
	artifactStore artifact.Store
	artifactCfg   artifact.Config
	logger        *slog.Logger
	// agentTimeout is the stub timeout for Phase 1 (0 = instant completion for tests)
	agentTimeout  time.Duration
}

// NewReconciler creates a sandbox manager reconciler.
func NewReconciler(store runstore.Store, provider SandboxProvider, enqueueEndpoint string, tokenMinter *gitproxy.TokenMinter, gitProxyURL string, artifactStore artifact.Store, artifactCfg artifact.Config, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		store:         store,
		provider:      provider,
		enqueue:       reconciler.NewEnqueueClient(enqueueEndpoint),
		tokenMinter:   tokenMinter,
		gitProxyURL:   gitProxyURL,
		artifactStore: artifactStore,
		artifactCfg:   artifactCfg,
		logger:        logger,
		agentTimeout:  30 * time.Second, // Phase 1 stub timeout
	}
}

// NewReconcilerWithEnqueuer creates a sandbox manager reconciler with a custom enqueuer (for testing).
func NewReconcilerWithEnqueuer(store runstore.Store, provider SandboxProvider, enqueuer Enqueuer, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		store:        store,
		provider:     provider,
		enqueue:      enqueuer,
		tokenMinter:  nil, // Tests don't need git-proxy
		gitProxyURL:  "",
		logger:       logger,
		agentTimeout: 0, // Instant completion for tests
	}
}

// Reconcile processes a stage run.
// Key format: "stage:{id}"
func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	stageID := req.Key[6:] // strip "stage:" prefix

	r.logger.Info("reconcile stage", "stage_id", stageID, "attempt", req.Attempt)

	// Fetch stage state
	stage, err := r.store.GetStage(ctx, stageID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get stage: %w", err)
	}

	// Terminal phases
	if stage.Phase == "succeeded" || stage.Phase == "failed" {
		r.logger.Info("stage in terminal phase", "stage_id", stageID, "phase", stage.Phase)
		return reconciler.Completed(), nil
	}

	// State machine
	switch stage.Phase {
	case "pending":
		return r.provisionSandbox(ctx, stage)
	case "provisioning_sandbox":
		return r.pollProvisioning(ctx, stage)
	case "running":
		return r.pollAgent(ctx, stage)
	case "collecting_output":
		return r.collectOutput(ctx, stage)
	default:
		return reconciler.ProcessResponse{}, fmt.Errorf("unknown phase: %s", stage.Phase)
	}
}

// provisionSandbox creates sandbox via provider.
func (r *Reconciler) provisionSandbox(ctx context.Context, stage *v1.StageRun) (reconciler.ProcessResponse, error) {
	r.logger.Info("provisioning sandbox", "stage_id", stage.ID)

	// Get run to extract resource bindings
	run, err := r.store.GetRun(ctx, stage.RunID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get run: %w", err)
	}

	// Build environment with git-proxy credentials
	env := make(map[string]string)
	if stage.AgentConfig.Environment != nil {
		for k, v := range stage.AgentConfig.Environment {
			env[k] = v
		}
	}

	// Mint git-proxy token if minter available
	if r.tokenMinter != nil && r.gitProxyURL != "" {
		token, err := r.mintGitToken(run, stage)
		if err != nil {
			r.logger.Warn("failed to mint git token", "stage_id", stage.ID, "error", err)
			// Non-fatal - continue without git access
		} else {
			env["FACTORY_GIT_TOKEN"] = token
			env["GIT_PROXY_URL"] = r.gitProxyURL
		}
	}

	spec := SandboxSpec{
		ID:          fmt.Sprintf("sb-%s", stage.ID),
		Image:       stage.AgentConfig.Image,
		Environment: env,
	}

	handle, err := r.provider.Create(ctx, spec)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("create sandbox: %w", err)
	}

	// Update stage
	stage.Phase = "provisioning_sandbox"
	stage.SandboxID = handle.ID
	if err := r.store.UpdateStage(ctx, stage); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
	}

	// Requeue to poll provisioning
	return reconciler.RequeueAfter(5 * time.Second), nil
}

// pollProvisioning polls sandbox until ready.
func (r *Reconciler) pollProvisioning(ctx context.Context, stage *v1.StageRun) (reconciler.ProcessResponse, error) {
	status, err := r.provider.Get(ctx, stage.SandboxID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get sandbox: %w", err)
	}

	if status.Phase == SandboxPhaseError {
		// Cleanup sandbox on provisioning failure
		if err := r.provider.Delete(ctx, stage.SandboxID); err != nil {
			r.logger.Error("delete failed sandbox", "stage_id", stage.ID, "error", err)
		}

		stage.Phase = "failed"
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage to failed: %w", err)
		}
		return reconciler.Completed(), nil
	}

	if !status.Ready {
		r.logger.Debug("sandbox not ready", "stage_id", stage.ID, "sandbox_phase", status.Phase)
		return reconciler.RequeueAfter(5 * time.Second), nil
	}

	// Sandbox ready - set up workspace and start agent
	if err := r.setupWorkspace(ctx, stage); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("setup workspace: %w", err)
	}

	// Start agent command
	if err := r.startAgent(ctx, stage); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("start agent: %w", err)
	}

	// Update phase
	now := time.Now().UTC()
	stage.Phase = "running"
	stage.StartedAt = &now
	if err := r.store.UpdateStage(ctx, stage); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
	}

	// Requeue to poll agent
	return reconciler.RequeueAfter(15 * time.Second), nil
}

// setupWorkspace prepares sandbox filesystem.
func (r *Reconciler) setupWorkspace(ctx context.Context, stage *v1.StageRun) error {
	r.logger.Info("setting up workspace", "stage_id", stage.ID)

	// Create /output dir
	cmd := []string{"mkdir", "-p", "/output"}
	if _, err := r.provider.Exec(ctx, stage.SandboxID, cmd, nil); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Create /workspace/inputs for upstream outputs
	cmd = []string{"mkdir", "-p", "/workspace/inputs"}
	if _, err := r.provider.Exec(ctx, stage.SandboxID, cmd, nil); err != nil {
		return fmt.Errorf("create inputs dir: %w", err)
	}

	// Load upstream stage outputs (fan-in)
	if err := r.loadUpstreamOutputs(ctx, stage); err != nil {
		return fmt.Errorf("load upstream outputs: %w", err)
	}

	// Capture initial git commit for git-based change detection
	if stage.AgentConfig.ChangeDetection == "git" || stage.AgentConfig.ChangeDetection == "auto" {
		if err := r.captureInitialGitCommit(ctx, stage); err != nil {
			r.logger.Warn("failed to capture initial git commit", "stage_id", stage.ID, "error", err)
			// Non-fatal - will fall back to explicit mode if needed
		}
	}

	return nil
}

// startAgent executes agent command in background.
func (r *Reconciler) startAgent(ctx context.Context, stage *v1.StageRun) error {
	r.logger.Info("starting agent", "stage_id", stage.ID, "command", stage.AgentConfig.Command)

	// Start agent in background via detached exec
	execID, err := r.provider.ExecDetached(ctx, stage.SandboxID, stage.AgentConfig.Command)
	if err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	// Store exec ID for status polling
	stage.AgentExecID = execID
	if err := r.store.UpdateStage(ctx, stage); err != nil {
		return fmt.Errorf("store exec ID: %w", err)
	}

	r.logger.Info("agent started", "stage_id", stage.ID, "exec_id", execID)
	return nil
}

// pollAgent checks if agent finished.
func (r *Reconciler) pollAgent(ctx context.Context, stage *v1.StageRun) (reconciler.ProcessResponse, error) {
	r.logger.Debug("polling agent", "stage_id", stage.ID, "exec_id", stage.AgentExecID)

	// Check agent process status
	status, err := r.provider.ExecStatus(ctx, stage.SandboxID, stage.AgentExecID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("check agent status: %w", err)
	}

	if status.Running {
		// Agent still running - check timeout
		if r.agentTimeout > 0 && stage.StartedAt != nil && time.Since(*stage.StartedAt) >= r.agentTimeout {
			r.logger.Warn("agent timeout exceeded", "stage_id", stage.ID, "timeout", r.agentTimeout)

			// Cleanup sandbox on timeout
			if err := r.provider.Delete(ctx, stage.SandboxID); err != nil {
				r.logger.Error("delete timed-out sandbox", "stage_id", stage.ID, "error", err)
			}

			stage.Phase = "failed"
			stage.Output = map[string]any{
				"error": fmt.Sprintf("agent timeout after %v", r.agentTimeout),
			}
			if err := r.store.UpdateStage(ctx, stage); err != nil {
				return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
			}
			return reconciler.Completed(), nil
		}

		// Adaptive polling: 5s early, 15s mid, 30s late
		elapsed := time.Since(*stage.StartedAt)
		var delay time.Duration
		if elapsed < 1*time.Minute {
			delay = 5 * time.Second
		} else if elapsed < 5*time.Minute {
			delay = 15 * time.Second
		} else {
			delay = 30 * time.Second
		}

		return reconciler.RequeueAfter(delay), nil
	}

	// Agent finished
	r.logger.Info("agent complete", "stage_id", stage.ID, "exit_code", status.ExitCode)

	if status.ExitCode != 0 {
		// Cleanup sandbox on agent failure
		if err := r.provider.Delete(ctx, stage.SandboxID); err != nil {
			r.logger.Error("delete failed sandbox", "stage_id", stage.ID, "error", err)
		}

		// Agent failed
		stage.Phase = "failed"
		stage.Output = map[string]any{
			"error": fmt.Sprintf("agent exited with code %d", status.ExitCode),
		}
		if err := r.store.UpdateStage(ctx, stage); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
		}
		return reconciler.Completed(), nil
	}

	// Success - proceed to output collection
	stage.Phase = "collecting_output"
	if err := r.store.UpdateStage(ctx, stage); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
	}
	return reconciler.RequeueAfter(0), nil
}

// collectOutput extracts /output from sandbox.
func (r *Reconciler) collectOutput(ctx context.Context, stage *v1.StageRun) (reconciler.ProcessResponse, error) {
	r.logger.Info("collecting output", "stage_id", stage.ID)

	// Try to read /output/output.json from sandbox
	outputData, err := r.provider.CopyFrom(ctx, stage.SandboxID, "/output/output.json")
	if err != nil {
		r.logger.Warn("failed to read output.json, using empty output", "stage_id", stage.ID, "error", err)
		stage.Output = map[string]interface{}{
			"type":    "report",
			"content": "no output.json found",
		}
	} else {
		// Parse JSON output
		var output map[string]interface{}
		if err := json.Unmarshal(outputData, &output); err != nil {
			r.logger.Error("invalid output.json format", "stage_id", stage.ID, "error", err)
			stage.Output = map[string]interface{}{
				"type":    "report",
				"content": fmt.Sprintf("invalid output.json: %v", err),
				"raw":     string(outputData),
			}
		} else {
			stage.Output = output
			r.logger.Info("output collected", "stage_id", stage.ID, "size", len(outputData))

			// Collect changes as artifact (git-based or explicit)
			if r.artifactStore != nil {
				changeDetection := stage.AgentConfig.ChangeDetection
				if changeDetection == "" {
					changeDetection = "auto"
				}

				switch changeDetection {
				case "git":
					if err := r.collectGitChanges(ctx, stage); err != nil {
						r.logger.Error("failed to collect git changes", "stage_id", stage.ID, "error", err)
					}
				case "explicit":
					if err := r.collectChangesAsArtifact(ctx, stage); err != nil {
						r.logger.Error("failed to collect explicit changes", "stage_id", stage.ID, "error", err)
					}
				case "auto":
					// Try git first, fall back to explicit
					if stage.InitialGitCommit != "" {
						if err := r.collectGitChanges(ctx, stage); err != nil {
							r.logger.Warn("git change detection failed, falling back to explicit", "stage_id", stage.ID, "error", err)
							if err := r.collectChangesAsArtifact(ctx, stage); err != nil {
								r.logger.Error("failed to collect explicit changes", "stage_id", stage.ID, "error", err)
							}
						}
					} else {
						if err := r.collectChangesAsArtifact(ctx, stage); err != nil {
							r.logger.Error("failed to collect explicit changes", "stage_id", stage.ID, "error", err)
						}
					}
				}
			}
		}
	}

	// Delete sandbox
	if err := r.provider.Delete(ctx, stage.SandboxID); err != nil {
		r.logger.Error("delete sandbox failed", "stage_id", stage.ID, "error", err)
	}

	// Enqueue into sf-output queue
	run, err := r.store.GetRun(ctx, stage.RunID)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get run: %w", err)
	}

	outputKey := fmt.Sprintf("output:%s", stage.ID)
	if err := r.enqueue.Enqueue(ctx, "sf-output", outputKey, run.Priority); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("enqueue output: %w", err)
	}

	// Mark stage succeeded
	now := time.Now().UTC()
	stage.Phase = "succeeded"
	stage.CompletedAt = &now
	if err := r.store.UpdateStage(ctx, stage); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("update stage: %w", err)
	}

	r.logger.Info("stage completed", "stage_id", stage.ID)
	return reconciler.Completed(), nil
}

// collectChangesAsArtifact packages /output/changes/ and uploads as artifact.
func (r *Reconciler) collectChangesAsArtifact(ctx context.Context, stage *v1.StageRun) error {
	// Create temp dir for changes
	tempDir, err := os.MkdirTemp("", "factory-changes-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Try to copy /output/changes/ from sandbox
	changesData, err := r.provider.CopyFrom(ctx, stage.SandboxID, "/output/changes/")
	if err != nil {
		// No changes directory - skip artifact storage
		return nil
	}

	estimatedSize := int64(len(changesData))
	r.logger.Info("uploading changes as artifact", "stage_id", stage.ID, "size", estimatedSize)

	// Save changes to temp file for tar packaging
	changesFile := tempDir + "/changes.tar"
	if err := os.WriteFile(changesFile, changesData, 0644); err != nil {
		return fmt.Errorf("write changes file: %w", err)
	}

	// Extract to directory for re-packaging
	changesDir := tempDir + "/extracted"
	if err := os.MkdirAll(changesDir, 0755); err != nil {
		return fmt.Errorf("create changes dir: %w", err)
	}

	// Package as tar.gz
	tarReader, _, err := artifact.CreateTarGz(changesDir)
	if err != nil {
		return fmt.Errorf("create tarball: %w", err)
	}

	// Upload to artifact storage
	artifactURL, err := r.artifactStore.Upload(ctx, stage.ID, tarReader)
	if err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}

	// Add artifact reference to output
	stage.Output["_artifact_url"] = artifactURL
	stage.Output["_artifact_type"] = "tar.gz"
	stage.Output["_artifact_size"] = estimatedSize

	r.logger.Info("artifact uploaded", "stage_id", stage.ID, "url", artifactURL, "size", estimatedSize)

	return nil
}

// loadUpstreamOutputs copies outputs from upstream stages to /workspace/inputs/.
func (r *Reconciler) loadUpstreamOutputs(ctx context.Context, stage *v1.StageRun) error {
	// Get run to find dependencies
	run, err := r.store.GetRun(ctx, stage.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	// Find this stage's spec to get dependsOn
	var stageSpec *v1.StageSpec
	for i := range run.PipelineSpec.Stages {
		if run.PipelineSpec.Stages[i].Name == stage.StageName {
			stageSpec = &run.PipelineSpec.Stages[i]
			break
		}
	}

	if stageSpec == nil {
		return fmt.Errorf("stage spec not found: %s", stage.StageName)
	}

	// No dependencies - nothing to load
	if len(stageSpec.DependsOn) == 0 {
		return nil
	}

	r.logger.Info("loading upstream outputs", "stage_id", stage.ID, "upstream_count", len(stageSpec.DependsOn))

	// Fetch all stages for this run
	allStages, err := r.store.ListStages(ctx, stage.RunID)
	if err != nil {
		return fmt.Errorf("list stages: %w", err)
	}

	// Build stage name -> stage map
	stageMap := make(map[string]*v1.StageRun)
	for i := range allStages {
		stageMap[allStages[i].StageName] = &allStages[i]
	}

	// For each dependency, copy its output to /workspace/inputs/
	for _, depName := range stageSpec.DependsOn {
		depStage, ok := stageMap[depName]
		if !ok {
			return fmt.Errorf("dependency stage not found: %s", depName)
		}

		if depStage.Output == nil {
			return fmt.Errorf("dependency %s has no output", depName)
		}

		// Validate stage name to prevent path traversal
		if err := validateStageName(depName); err != nil {
			return fmt.Errorf("invalid dependency name %s: %w", depName, err)
		}

		// Serialize output to JSON
		outputJSON, err := json.Marshal(depStage.Output)
		if err != nil {
			return fmt.Errorf("marshal output for %s: %w", depName, err)
		}

		// Validate output size (max 10MB)
		const maxOutputSize = 10 * 1024 * 1024 // 10MB
		if len(outputJSON) > maxOutputSize {
			return fmt.Errorf("output from %s exceeds max size: %d bytes (max %d)", depName, len(outputJSON), maxOutputSize)
		}

		// Create directory for this upstream stage
		dirPath := fmt.Sprintf("/workspace/inputs/%s", depName)
		cmd := []string{"mkdir", "-p", dirPath}
		if _, err := r.provider.Exec(ctx, stage.SandboxID, cmd, nil); err != nil {
			return fmt.Errorf("create input dir %s: %w", dirPath, err)
		}

		// Write output.json
		outputPath := fmt.Sprintf("%s/output.json", dirPath)
		if err := r.provider.CopyTo(ctx, stage.SandboxID, outputPath, outputJSON); err != nil {
			return fmt.Errorf("copy output for %s: %w", depName, err)
		}

		r.logger.Info("loaded upstream output", "stage_id", stage.ID, "upstream", depName, "size", len(outputJSON))
	}

	return nil
}

// validateStageName ensures stage name is safe for filesystem paths.
func validateStageName(name string) error {
	if name == "" {
		return fmt.Errorf("empty stage name")
	}

	// Check for path traversal attempts
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("stage name contains path separator")
	}

	if strings.Contains(name, "..") {
		return fmt.Errorf("stage name contains parent directory reference")
	}

	// Require alphanumeric + dash/underscore only
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return fmt.Errorf("stage name contains invalid character: %c", r)
		}
	}

	return nil
}

// mintGitToken creates a scoped git-proxy token for the sandbox.
func (r *Reconciler) mintGitToken(run *v1.PipelineRun, stage *v1.StageRun) (string, error) {
	// Build resource access map from stage's declared resources
	resources := make(map[string]gitproxy.Access)

	// Extract resources from pipeline spec
	for _, resourceName := range stage.AgentConfig.Resources {
		// Look up resource in pipeline spec
		resource, ok := run.PipelineSpec.Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s declared in stage but not in pipeline spec", resourceName)
		}

		// Get bound URL from run - fail if missing
		url, ok := run.ResourceBindings[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s declared but not bound", resourceName)
		}

		access := gitproxy.Access{
			Type:  resource.Type,
			Level: resource.Access,
			URL:   url,
			// Ref extracted from resource binding if needed
		}

		resources[resourceName] = access
	}

	// Mint token with expiry matching sandbox lifetime
	// Use max of agentTimeout (expected runtime) or 10 minutes minimum
	tokenExpiry := 10 * time.Minute
	if r.agentTimeout > 0 && r.agentTimeout < tokenExpiry {
		tokenExpiry = r.agentTimeout
	}
	// Add buffer for sandbox provisioning + output collection
	tokenExpiry += 5 * time.Minute

	claims := gitproxy.TokenClaims{
		RunID:      run.ID,
		StageID:    stage.ID,
		Resources:  resources,
		ExpiresAt:  time.Now().Add(tokenExpiry).Unix(),
	}

	return r.tokenMinter.Mint(claims)
}

// captureInitialGitCommit stores the current git commit for change detection.
func (r *Reconciler) captureInitialGitCommit(ctx context.Context, stage *v1.StageRun) error {
	// Check if /workspace is a git repo
	cmd := []string{"git", "-C", "/workspace", "rev-parse", "--git-dir"}
	result, err := r.provider.Exec(ctx, stage.SandboxID, cmd, nil)
	if err != nil || result.ExitCode != 0 {
		// Not a git repo - skip
		return nil
	}

	if len(result.Stdout) == 0 {
		return nil
	}

	// Get current commit hash
	cmd = []string{"git", "-C", "/workspace", "rev-parse", "HEAD"}
	commitResult, err := r.provider.Exec(ctx, stage.SandboxID, cmd, nil)
	if err != nil {
		if strings.Contains(err.Error(), "unknown revision") || strings.Contains(err.Error(), "bad revision") {
			// Fresh repo, no commits yet - skip
			return nil
		}
		return fmt.Errorf("get git commit: %w", err)
	}
	if commitResult.ExitCode != 0 {
		// Git command failed (likely no commits)
		return nil
	}

	commitHash := strings.TrimSpace(string(commitResult.Stdout))
	if commitHash == "" {
		return fmt.Errorf("empty commit hash")
	}

	// Validate commit hash format
	if len(commitHash) < 7 || len(commitHash) > 40 {
		return fmt.Errorf("invalid commit hash length: %s", commitHash)
	}
	for _, c := range commitHash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return fmt.Errorf("invalid commit hash format: %s", commitHash)
		}
	}

	r.logger.Info("captured initial git commit", "stage_id", stage.ID, "commit", commitHash)

	// Store in stage
	stage.InitialGitCommit = commitHash
	if err := r.store.UpdateStage(ctx, stage); err != nil {
		return fmt.Errorf("update stage with initial commit: %w", err)
	}

	return nil
}

// collectGitChanges detects changed files via git and uploads as artifact.
func (r *Reconciler) collectGitChanges(ctx context.Context, stage *v1.StageRun) error {
	if stage.InitialGitCommit == "" {
		return fmt.Errorf("no initial commit tracked")
	}

	r.logger.Info("collecting git changes", "stage_id", stage.ID, "initial_commit", stage.InitialGitCommit)

	// Get list of changed files
	cmd := []string{
		"git", "-C", "/workspace",
		"diff", "--name-only",
		fmt.Sprintf("%s..HEAD", stage.InitialGitCommit),
	}
	filesResult, err := r.provider.Exec(ctx, stage.SandboxID, cmd, nil)
	if err != nil {
		return fmt.Errorf("get changed files: %w", err)
	}
	if filesResult.ExitCode != 0 {
		return fmt.Errorf("git diff failed with exit code %d: %s", filesResult.ExitCode, string(filesResult.Stderr))
	}

	filesStr := strings.TrimSpace(string(filesResult.Stdout))
	if filesStr == "" {
		r.logger.Info("no git changes detected", "stage_id", stage.ID)
		return nil
	}
	changedFiles := strings.Split(filesStr, "\n")

	// Enforce file count limit
	const maxChangedFiles = 5000
	if len(changedFiles) > maxChangedFiles {
		return fmt.Errorf("too many changed files: %d (max %d)", len(changedFiles), maxChangedFiles)
	}

	r.logger.Info("detected git changes", "stage_id", stage.ID, "file_count", len(changedFiles))

	// Create temp dir for collecting changed files
	tempDir, err := os.MkdirTemp("", "factory-git-changes-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	changesDir := tempDir + "/changes"
	if err := os.MkdirAll(changesDir, 0755); err != nil {
		return fmt.Errorf("create changes dir: %w", err)
	}

	// Copy each changed file from sandbox
	const maxFileSize = 50 * 1024 * 1024       // 50MB per file
	const maxTotalSize = 500 * 1024 * 1024     // 500MB total
	var copiedCount int
	var totalSize int64

	for _, file := range changedFiles {
		if file == "" {
			continue
		}

		// Path traversal protection
		cleanFile := filepath.Clean(file)
		if strings.Contains(cleanFile, "..") || filepath.IsAbs(cleanFile) || strings.Contains(file, "\x00") {
			r.logger.Warn("skipping suspicious file path", "stage_id", stage.ID, "file", file)
			continue
		}

		srcPath := fmt.Sprintf("/workspace/%s", cleanFile)
		dstPath := filepath.Join(changesDir, cleanFile)

		// Double-check result stays within changesDir
		if !strings.HasPrefix(filepath.Clean(dstPath), filepath.Clean(changesDir)+string(os.PathSeparator)) {
			r.logger.Warn("path traversal blocked", "stage_id", stage.ID, "file", file)
			continue
		}

		// Create parent directory
		dstDir := filepath.Dir(dstPath)
		if err := os.MkdirAll(dstDir, 0755); err != nil {
			r.logger.Warn("failed to create dir for changed file", "stage_id", stage.ID, "file", file, "error", err)
			continue
		}

		// Copy file from sandbox
		fileData, err := r.provider.CopyFrom(ctx, stage.SandboxID, srcPath)
		if err != nil {
			r.logger.Warn("failed to copy changed file", "stage_id", stage.ID, "file", file, "error", err)
			continue
		}

		// Enforce size limits
		if len(fileData) > maxFileSize {
			r.logger.Warn("file exceeds size limit", "stage_id", stage.ID, "file", file, "size", len(fileData))
			continue
		}

		totalSize += int64(len(fileData))
		if totalSize > maxTotalSize {
			return fmt.Errorf("total changes exceed limit: %d bytes (max %d)", totalSize, maxTotalSize)
		}

		if err := os.WriteFile(dstPath, fileData, 0644); err != nil {
			r.logger.Warn("failed to write changed file", "stage_id", stage.ID, "file", file, "error", err)
			continue
		}

		copiedCount++
	}

	if copiedCount == 0 {
		r.logger.Warn("no files successfully copied", "stage_id", stage.ID)
		return nil
	}

	r.logger.Info("copied changed files", "stage_id", stage.ID, "count", copiedCount)

	// Package as tar.gz
	tarReader, estimatedSize, err := artifact.CreateTarGz(changesDir)
	if err != nil {
		return fmt.Errorf("create tarball: %w", err)
	}

	// Upload to artifact storage
	artifactURL, err := r.artifactStore.Upload(ctx, stage.ID, tarReader)
	if err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}

	// Add artifact reference to output
	stage.Output["_artifact_url"] = artifactURL
	stage.Output["_artifact_type"] = "tar.gz"
	stage.Output["_artifact_size"] = estimatedSize
	stage.Output["_change_detection"] = "git"
	stage.Output["_changed_files"] = changedFiles
	stage.Output["_initial_commit"] = stage.InitialGitCommit

	r.logger.Info("git changes artifact uploaded", "stage_id", stage.ID, "url", artifactURL, "files", copiedCount)

	return nil
}
