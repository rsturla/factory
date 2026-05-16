package output

import (
	"context"
	"fmt"
)

const (
	maxReportTitleLength   = 1024                // 1KB max title
	maxReportContentLength = 10 * 1024 * 1024    // 10MB max content
)

// ReportHandler processes report-type outputs.
// Reports are stored in the stage output, no external action needed.
type ReportHandler struct{}

// Validate checks report format.
func (h *ReportHandler) Validate(ctx context.Context, output map[string]any) error {
	// Report can be:
	// - {"content": "markdown text"}
	// - {"title": "Report Title", "content": "body", "format": "markdown"}
	// - arbitrary structured data

	if output == nil {
		return fmt.Errorf("output is nil")
	}

	// If content field exists, validate it's a string with length limit
	if content, ok := output["content"]; ok {
		contentStr, isString := content.(string)
		if !isString {
			return fmt.Errorf("content field must be string")
		}
		if len(contentStr) > maxReportContentLength {
			return fmt.Errorf("content exceeds max length of %d bytes", maxReportContentLength)
		}
	}

	// If title field exists, validate it's a string with length limit
	if title, ok := output["title"]; ok {
		titleStr, isString := title.(string)
		if !isString {
			return fmt.Errorf("title field must be string")
		}
		if len(titleStr) > maxReportTitleLength {
			return fmt.Errorf("title exceeds max length of %d bytes", maxReportTitleLength)
		}
	}

	return nil
}

// Execute stores the report (already stored in stage output by sandbox manager).
func (h *ReportHandler) Execute(ctx context.Context, params ExecuteParams) (*ExecuteResult, error) {
	// Report is already in params.Output, no action needed
	message := "report stored"
	if title, ok := params.Output["title"].(string); ok {
		message = fmt.Sprintf("report stored: %s", title)
	}

	return &ExecuteResult{
		Success: true,
		Message: message,
		Details: params.Output,
	}, nil
}
