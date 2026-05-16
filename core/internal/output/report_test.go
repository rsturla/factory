package output

import (
	"context"
	"strings"
	"testing"
)

func TestReportHandler_Validate(t *testing.T) {
	handler := &ReportHandler{}
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
			name:      "valid simple report",
			output:    map[string]any{"content": "test content"},
			expectErr: false,
		},
		{
			name:      "valid report with title",
			output:    map[string]any{"title": "Report", "content": "body"},
			expectErr: false,
		},
		{
			name:      "content not string",
			output:    map[string]any{"content": 123},
			expectErr: true,
		},
		{
			name:      "title not string",
			output:    map[string]any{"title": true},
			expectErr: true,
		},
		{
			name:      "content too large",
			output:    map[string]any{"content": strings.Repeat("x", maxReportContentLength+1)},
			expectErr: true,
		},
		{
			name:      "title too large",
			output:    map[string]any{"title": strings.Repeat("x", maxReportTitleLength+1)},
			expectErr: true,
		},
		{
			name:      "content at max limit",
			output:    map[string]any{"content": strings.Repeat("x", maxReportContentLength)},
			expectErr: false,
		},
		{
			name:      "arbitrary structured data",
			output:    map[string]any{"metric": "value", "count": 42},
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

func TestReportHandler_Execute(t *testing.T) {
	handler := &ReportHandler{}
	ctx := context.Background()

	output := map[string]any{
		"title":   "Test Report",
		"content": "This is a test",
	}

	result, err := handler.Execute(ctx, ExecuteParams{
		Output: output,
	})

	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if !result.Success {
		t.Error("expected success=true")
	}

	if !strings.Contains(result.Message, "Test Report") {
		t.Errorf("expected message to contain title, got: %s", result.Message)
	}
}
