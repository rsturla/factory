package prompt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
)

// Renderer renders prompt templates with custom functions.
type Renderer struct {
	pipelineDir string
	provider    sandbox.SandboxProvider
	sandboxID   string
}

// NewRenderer creates a prompt renderer.
func NewRenderer(pipelineDir string, provider sandbox.SandboxProvider, sandboxID string) *Renderer {
	return &Renderer{
		pipelineDir: pipelineDir,
		provider:    provider,
		sandboxID:   sandboxID,
	}
}

// Render processes a prompt template with the given context.
func (r *Renderer) Render(ctx context.Context, promptPath string, data TemplateData) (string, error) {
	// Read prompt template
	templateContent, err := r.readPromptFile(promptPath)
	if err != nil {
		return "", fmt.Errorf("read prompt template: %w", err)
	}

	// Create template with custom functions
	tmpl, err := template.New("prompt").Funcs(r.funcMap(ctx, data)).Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	// Execute template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

// TemplateData contains data available to prompt templates.
type TemplateData struct {
	Params    map[string]string      // pipeline parameters
	Resources map[string]v1.Resource // resource definitions
	Stage     v1.StageSpec           // current stage spec
}

// funcMap returns custom template functions.
func (r *Renderer) funcMap(ctx context.Context, data TemplateData) template.FuncMap {
	return template.FuncMap{
		"include": r.includeFunc,
		"file":    r.fileFunc(ctx),
		"exec":    r.execFunc(ctx),
		"param":   r.paramFunc(data),
	}
}

// includeFunc loads and includes another template file.
// Usage: {{include "prompts/common.md"}}
func (r *Renderer) includeFunc(path string) (string, error) {
	// Resolve path relative to pipeline directory
	fullPath := filepath.Join(r.pipelineDir, path)

	// Security: verify path is within pipeline directory
	absBase, err := filepath.Abs(r.pipelineDir)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}

	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve include path: %w", err)
	}

	relPath, err := filepath.Rel(absBase, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("include path outside pipeline directory: %s", path)
	}

	// Read file
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read include file: %w", err)
	}

	return string(content), nil
}

// fileFunc reads a file from the sandbox filesystem.
// Usage: {{file "/workspace/repo/spec.json"}}
func (r *Renderer) fileFunc(ctx context.Context) func(string) (string, error) {
	return func(path string) (string, error) {
		// Security: only allow /workspace paths
		if !strings.HasPrefix(path, "/workspace/") {
			return "", fmt.Errorf("file path must start with /workspace/")
		}

		// Read from sandbox
		content, err := r.provider.CopyFrom(ctx, r.sandboxID, path)
		if err != nil {
			return "", fmt.Errorf("read file from sandbox: %w", err)
		}

		return string(content), nil
	}
}

// execFunc executes a command in the sandbox and returns output.
// Usage: {{exec "git -C /workspace/repo log --oneline -10"}}
func (r *Renderer) execFunc(ctx context.Context) func(string) (string, error) {
	return func(cmdStr string) (string, error) {
		// Parse command string into args
		args := strings.Fields(cmdStr)
		if len(args) == 0 {
			return "", fmt.Errorf("empty command")
		}

		// Execute in sandbox
		result, err := r.provider.Exec(ctx, r.sandboxID, args, nil)
		if err != nil {
			return "", fmt.Errorf("exec failed: %w", err)
		}

		if result.ExitCode != 0 {
			return "", fmt.Errorf("command failed with exit code %d: %s", result.ExitCode, string(result.Stderr))
		}

		return string(result.Stdout), nil
	}
}

// paramFunc retrieves a parameter value.
// Usage: {{param "cve_id"}}
func (r *Renderer) paramFunc(data TemplateData) func(string) string {
	return func(key string) string {
		return data.Params[key]
	}
}

// readPromptFile reads the prompt template file from pipeline directory.
func (r *Renderer) readPromptFile(promptPath string) (string, error) {
	fullPath := filepath.Join(r.pipelineDir, promptPath)

	// Security check
	absBase, err := filepath.Abs(r.pipelineDir)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}

	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve prompt path: %w", err)
	}

	relPath, err := filepath.Rel(absBase, absPath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("prompt path outside pipeline directory: %s", promptPath)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read prompt file: %w", err)
	}

	return string(content), nil
}
