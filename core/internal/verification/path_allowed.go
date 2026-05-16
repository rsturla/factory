package verification

import (
	"context"
	"fmt"
	"path/filepath"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// PathAllowedGate validates that modified paths are allowed.
type PathAllowedGate struct {
	DenyPatterns []string // glob patterns to deny
}

// Name returns gate name.
func (g *PathAllowedGate) Name() string {
	return "path_allowed"
}

// Check validates paths against deny list.
func (g *PathAllowedGate) Check(ctx context.Context, stage *v1.StageRun) error {
	if stage.Output == nil {
		return nil
	}

	// Extract file list from output
	files, ok := stage.Output["files"]
	if !ok {
		// No file list - skip check
		return nil
	}

	fileList, ok := files.([]any)
	if !ok {
		return nil
	}

	// Check each file against deny patterns
	for _, f := range fileList {
		var path string

		switch file := f.(type) {
		case string:
			path = file
		case map[string]any:
			if p, ok := file["path"].(string); ok {
				path = p
			}
		}

		if path == "" {
			continue
		}

		// Check against deny patterns
		for _, pattern := range g.DenyPatterns {
			matched, err := filepath.Match(pattern, filepath.Base(path))
			if err != nil {
				return fmt.Errorf("invalid pattern %s: %w", pattern, err)
			}

			if matched {
				return fmt.Errorf("path denied by policy: %s (matches %s)", path, pattern)
			}

			// Check full path for directory patterns
			if matched, _ := filepath.Match(pattern, path); matched {
				return fmt.Errorf("path denied by policy: %s (matches %s)", path, pattern)
			}
		}
	}

	return nil
}
