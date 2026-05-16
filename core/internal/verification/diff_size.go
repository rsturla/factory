package verification

import (
	"context"
	"fmt"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// DiffSizeGate enforces limits on change size.
type DiffSizeGate struct {
	MaxLines int
	MaxFiles int
}

// Name returns gate name.
func (g *DiffSizeGate) Name() string {
	return "diff_size"
}

// Check validates diff size limits.
func (g *DiffSizeGate) Check(ctx context.Context, stage *v1.StageRun) error {
	if stage.Output == nil {
		return nil
	}

	// Extract diff metadata from output
	// For PR/patch outputs: output should contain file list and line counts
	files, ok := stage.Output["files"]
	if !ok {
		// No file list - skip check (not applicable for all output types)
		return nil
	}

	fileList, ok := files.([]any)
	if !ok {
		return nil
	}

	fileCount := len(fileList)
	if g.MaxFiles > 0 && fileCount > g.MaxFiles {
		return fmt.Errorf("too many files modified: %d exceeds limit of %d", fileCount, g.MaxFiles)
	}

	// Count total lines changed
	totalLines := 0
	for _, f := range fileList {
		fileMap, ok := f.(map[string]any)
		if !ok {
			continue
		}

		if lines, ok := fileMap["lines_changed"].(float64); ok {
			totalLines += int(lines)
		}
	}

	if g.MaxLines > 0 && totalLines > g.MaxLines {
		return fmt.Errorf("too many lines changed: %d exceeds limit of %d", totalLines, g.MaxLines)
	}

	return nil
}
