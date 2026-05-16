package output

import (
	"context"
	"fmt"
)

// PRHandler processes pull request outputs.
// Creates branch, commits changes, opens PR via git provider.
type PRHandler struct {
	// TODO: Add GitHub/GitLab client when available
}

// Validate checks PR output format.
func (h *PRHandler) Validate(ctx context.Context, output map[string]any) error {
	if output == nil {
		return fmt.Errorf("output is nil")
	}

	// PR output should contain:
	// - files: list of file changes
	// - title: PR title
	// - body: PR description (optional)
	// - branch: target branch name (optional, auto-generated if missing)

	files, ok := output["files"]
	if !ok {
		return fmt.Errorf("missing required field: files")
	}

	fileList, ok := files.([]any)
	if !ok {
		return fmt.Errorf("files must be an array")
	}

	if len(fileList) == 0 {
		return fmt.Errorf("files array is empty")
	}

	// Validate each file entry
	for i, f := range fileList {
		fileMap, ok := f.(map[string]any)
		if !ok {
			return fmt.Errorf("file entry %d must be an object", i)
		}

		path, ok := fileMap["path"].(string)
		if !ok || path == "" {
			return fmt.Errorf("file entry %d missing path", i)
		}

		content, ok := fileMap["content"].(string)
		if !ok {
			return fmt.Errorf("file entry %d missing content", i)
		}

		// Validate content size
		if len(content) > 1024*1024 { // 1MB per file
			return fmt.Errorf("file %s content exceeds 1MB", path)
		}
	}

	// Validate title
	title, ok := output["title"].(string)
	if !ok || title == "" {
		return fmt.Errorf("missing required field: title")
	}

	if len(title) > 256 {
		return fmt.Errorf("title exceeds 256 characters")
	}

	// Body is optional
	if body, ok := output["body"].(string); ok {
		if len(body) > 65536 { // 64KB max body
			return fmt.Errorf("body exceeds 64KB")
		}
	}

	return nil
}

// Execute creates branch, commits, and opens PR.
func (h *PRHandler) Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error) {
	// Extract PR metadata
	title := params.Output["title"].(string)
	body, _ := params.Output["body"].(string)
	files := params.Output["files"].([]any)

	// Generate branch name if not provided
	branchName, ok := params.Output["branch"].(string)
	if !ok || branchName == "" {
		branchName = fmt.Sprintf("factory/%s", params.StageRun.ID)
	}

	// Phase 2 MVP: validate and prepare PR data, actual creation deferred
	// Phase 3: integrate with GitHub/GitLab API clients

	details := map[string]any{
		"branch":     branchName,
		"title":      title,
		"body":       body,
		"file_count": len(files),
		"status":     "validated",
		"note":       "PR creation requires GitHub/GitLab client integration (Phase 3)",
	}

	return &ExecuteResult{
		Success: true,
		Message: fmt.Sprintf("PR validated: %s", title),
		Details: details,
	}, nil
}
