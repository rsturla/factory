package output

import (
	"context"
	"strings"
	"testing"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

func TestPRHandler_Validate(t *testing.T) {
	handler := &PRHandler{}
	ctx := context.Background()

	tests := []struct {
		name      string
		output    map[string]any
		expectErr bool
	}{
		{
			name:      "nil output",
			output:    nil,
			expectErr: true,
		},
		{
			name: "valid PR output",
			output: map[string]any{
				"title": "Fix bug in authentication",
				"body":  "This PR fixes the auth bug",
				"files": []any{
					map[string]any{
						"path":    "src/auth.go",
						"content": "package auth\n\nfunc Login() {}",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "missing files",
			output: map[string]any{
				"title": "Fix bug",
			},
			expectErr: true,
		},
		{
			name: "empty files array",
			output: map[string]any{
				"title": "Fix bug",
				"files": []any{},
			},
			expectErr: true,
		},
		{
			name: "missing title",
			output: map[string]any{
				"files": []any{
					map[string]any{
						"path":    "src/main.go",
						"content": "package main",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "title too long",
			output: map[string]any{
				"title": strings.Repeat("x", 257),
				"files": []any{
					map[string]any{
						"path":    "src/main.go",
						"content": "package main",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "body too long",
			output: map[string]any{
				"title": "Fix bug",
				"body":  strings.Repeat("x", 65537),
				"files": []any{
					map[string]any{
						"path":    "src/main.go",
						"content": "package main",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "file missing path",
			output: map[string]any{
				"title": "Fix bug",
				"files": []any{
					map[string]any{
						"content": "package main",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "file missing content",
			output: map[string]any{
				"title": "Fix bug",
				"files": []any{
					map[string]any{
						"path": "src/main.go",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "file content too large",
			output: map[string]any{
				"title": "Fix bug",
				"files": []any{
					map[string]any{
						"path":    "src/large.go",
						"content": strings.Repeat("x", 1024*1024+1),
					},
				},
			},
			expectErr: true,
		},
		{
			name: "multiple files valid",
			output: map[string]any{
				"title": "Refactor code",
				"files": []any{
					map[string]any{
						"path":    "src/auth.go",
						"content": "package auth",
					},
					map[string]any{
						"path":    "src/main.go",
						"content": "package main",
					},
				},
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler.Validate(ctx, tt.output)
			if tt.expectErr && err == nil {
				t.Errorf("expected error for %v", tt.output)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPRHandler_Execute(t *testing.T) {
	handler := &PRHandler{}
	ctx := context.Background()

	output := map[string]any{
		"title": "Fix authentication bug",
		"body":  "This PR fixes the bug in the login flow",
		"files": []any{
			map[string]any{
				"path":    "src/auth.go",
				"content": "package auth\n\nfunc Login() {}",
			},
		},
	}

	stage := &v1.StageRun{
		ID: "stage-123",
	}

	result, err := handler.Execute(ctx, ExecuteParams{
		StageRun: stage,
		Output:   output,
	})

	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if !result.Success {
		t.Error("expected success=true")
	}

	if !strings.Contains(result.Message, "Fix authentication bug") {
		t.Errorf("expected message to contain title, got: %s", result.Message)
	}

	// Verify details contain expected fields
	branch, ok := result.Details["branch"].(string)
	if !ok || branch == "" {
		t.Error("expected branch in details")
	}

	fileCount, ok := result.Details["file_count"].(int)
	if !ok || fileCount != 1 {
		t.Errorf("expected file_count=1, got %v", fileCount)
	}
}
