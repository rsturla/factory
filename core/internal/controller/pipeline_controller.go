package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// PipelineController manages deployed pipelines and their triggers.
type PipelineController struct {
	logger   *slog.Logger
	pipelines map[string]*Pipeline
	mu       sync.RWMutex
}

// Pipeline represents a deployed pipeline.
type Pipeline struct {
	Name     string
	RepoURL  string
	Branch   string
	Path     string
	Triggers []TriggerDefinition
}

// TriggerDefinition represents a pipeline trigger.
type TriggerDefinition struct {
	Type   string                 `json:"type"`
	Config map[string]interface{} `json:"config"`
}

// NewPipelineController creates pipeline controller.
func NewPipelineController(logger *slog.Logger) *PipelineController {
	return &PipelineController{
		logger:    logger,
		pipelines: make(map[string]*Pipeline),
	}
}

// Start starts controller.
func (c *PipelineController) Start(ctx context.Context) error {
	c.logger.Info("pipeline controller starting")

	// Load pipeline deployments from config
	if err := c.loadPipelines(ctx); err != nil {
		return fmt.Errorf("load pipelines: %w", err)
	}

	// Start trigger handlers
	for _, pipeline := range c.pipelines {
		for _, trigger := range pipeline.Triggers {
			c.startTrigger(ctx, pipeline, trigger)
		}
	}

	<-ctx.Done()
	c.logger.Info("pipeline controller stopped")
	return nil
}

// loadPipelines loads pipeline deployments from environment config.
func (c *PipelineController) loadPipelines(ctx context.Context) error {
	// Read config from env var (JSON array of pipeline deployments)
	configJSON := os.Getenv("PIPELINE_DEPLOYMENTS")
	if configJSON == "" {
		c.logger.Warn("no pipeline deployments configured")
		return nil
	}

	var deployments []struct {
		Name    string `json:"name"`
		RepoURL string `json:"repo_url"`
		Branch  string `json:"branch"`
		Path    string `json:"path"`
	}

	if err := json.Unmarshal([]byte(configJSON), &deployments); err != nil {
		return fmt.Errorf("parse deployments: %w", err)
	}

	// Extract triggers from each pipeline
	for _, dep := range deployments {
		pipeline, err := c.extractPipeline(ctx, dep.Name, dep.RepoURL, dep.Branch, dep.Path)
		if err != nil {
			c.logger.Error("extract pipeline failed", "name", dep.Name, "error", err)
			continue
		}

		c.mu.Lock()
		c.pipelines[dep.Name] = pipeline
		c.mu.Unlock()

		c.logger.Info("pipeline loaded",
			"name", pipeline.Name,
			"triggers", len(pipeline.Triggers),
		)
	}

	return nil
}

// extractPipeline clones repo and extracts trigger definitions.
func (c *PipelineController) extractPipeline(ctx context.Context, name, repoURL, branch, path string) (*Pipeline, error) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("pipeline-%s-*", name))
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone repo
	cloneCmd := exec.CommandContext(ctx, "git", "clone",
		"--depth=1",
		"--branch", branch,
		repoURL,
		tmpDir,
	)
	if err := cloneCmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone: %w", err)
	}

	// Run pipeline in dry-run mode to extract triggers
	pipelinePath := filepath.Join(tmpDir, path, "pipeline.ts")

	// Use bun to run pipeline with --dry-run flag
	extractCmd := exec.CommandContext(ctx, "bun", "run", pipelinePath, "--dry-run")
	extractCmd.Dir = filepath.Dir(pipelinePath)

	output, err := extractCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("extract triggers: %w: %s", err, output)
	}

	// Parse trigger JSON from stdout
	var triggers []TriggerDefinition
	if err := json.Unmarshal(output, &triggers); err != nil {
		return nil, fmt.Errorf("parse triggers: %w", err)
	}

	return &Pipeline{
		Name:     name,
		RepoURL:  repoURL,
		Branch:   branch,
		Path:     path,
		Triggers: triggers,
	}, nil
}

// startTrigger starts trigger handler.
func (c *PipelineController) startTrigger(ctx context.Context, pipeline *Pipeline, trigger TriggerDefinition) {
	switch trigger.Type {
	case "jira":
		c.startJiraTrigger(ctx, pipeline, trigger)
	case "webhook":
		c.startWebhookTrigger(ctx, pipeline, trigger)
	case "schedule":
		c.startScheduleTrigger(ctx, pipeline, trigger)
	case "github":
		c.startGitHubTrigger(ctx, pipeline, trigger)
	case "manual":
		// Manual triggers handled via API
		c.logger.Info("manual trigger registered", "pipeline", pipeline.Name)
	default:
		c.logger.Warn("unknown trigger type", "type", trigger.Type, "pipeline", pipeline.Name)
	}
}

// startJiraTrigger starts Jira polling trigger.
func (c *PipelineController) startJiraTrigger(ctx context.Context, pipeline *Pipeline, trigger TriggerDefinition) {
	query := trigger.Config["query"].(string)
	pollInterval := trigger.Config["poll"].(string)

	duration, err := time.ParseDuration(pollInterval)
	if err != nil {
		c.logger.Error("invalid poll interval", "interval", pollInterval, "error", err)
		return
	}

	c.logger.Info("starting jira trigger",
		"pipeline", pipeline.Name,
		"query", query,
		"poll", pollInterval,
	)

	go func() {
		ticker := time.NewTicker(duration)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.pollJira(ctx, pipeline, query, trigger); err != nil {
					c.logger.Error("jira poll failed", "pipeline", pipeline.Name, "error", err)
				}
			}
		}
	}()
}

// pollJira polls Jira API for matching issues.
func (c *PipelineController) pollJira(ctx context.Context, pipeline *Pipeline, query string, trigger TriggerDefinition) error {
	// TODO: Implement Jira API polling
	// For now, just log
	c.logger.Debug("polling jira", "pipeline", pipeline.Name, "query", query)
	return nil
}

// startWebhookTrigger registers webhook endpoint.
func (c *PipelineController) startWebhookTrigger(ctx context.Context, pipeline *Pipeline, trigger TriggerDefinition) {
	path := trigger.Config["path"].(string)

	c.logger.Info("starting webhook trigger",
		"pipeline", pipeline.Name,
		"path", path,
	)

	// TODO: Register HTTP handler
}

// startScheduleTrigger starts cron-based trigger.
func (c *PipelineController) startScheduleTrigger(ctx context.Context, pipeline *Pipeline, trigger TriggerDefinition) {
	cronSpec := trigger.Config["cron"].(string)

	c.logger.Info("starting schedule trigger",
		"pipeline", pipeline.Name,
		"cron", cronSpec,
	)

	// TODO: Implement cron scheduler
}

// startGitHubTrigger starts GitHub webhook trigger.
func (c *PipelineController) startGitHubTrigger(ctx context.Context, pipeline *Pipeline, trigger TriggerDefinition) {
	event := trigger.Config["event"].(string)

	c.logger.Info("starting github trigger",
		"pipeline", pipeline.Name,
		"event", event,
	)

	// TODO: Register GitHub webhook handler
}

// spawnPipelineJob creates K8s Job to run pipeline.
func (c *PipelineController) spawnPipelineJob(ctx context.Context, pipeline *Pipeline, params map[string]string) error {
	c.logger.Info("spawning pipeline job",
		"pipeline", pipeline.Name,
		"params", params,
	)

	// TODO: Generate K8s Job manifest and create via client-go
	return nil
}
